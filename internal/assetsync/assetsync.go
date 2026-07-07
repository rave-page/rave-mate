// Package assetsync replicates local assets across paired rave-mate instances over the existing
// peer link. First surface: Motion Studio recordings - pull a peer's recordings, diff by
// name+sha256, and write the missing/changed ones locally (idempotent).
package assetsync

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
)

// MotionClient is the peer-motion RPC subset assetsync needs (decoupled for tests). Satisfied by
// *remotectl.Client.
type MotionClient interface {
	MotionList(ctx context.Context) (remotectl.MotionListResult, error)
	MotionGet(ctx context.Context, name string) (remotectl.MotionGetResult, error)
}

// PeerManager is the peerlink.Manager subset the all-peers driver needs (decoupled for tests).
type PeerManager interface {
	Connections() []peerlink.ConnInfo
}

// ReconcileResult reports one peer→local motion reconcile pass.
type ReconcileResult struct {
	Pulled  []string          // names written (new or changed)
	Skipped int               // already up-to-date (local sha256 matched)
	Errors  map[string]string // name → error (per-recording failures; non-fatal)
}

// ReconcileMotion pulls the peer's motion list and writes every recording whose local copy is
// missing or whose sha256 differs. Idempotent: unchanged recordings are skipped. Per-recording
// failures are collected in the result; only a failed list (or bad dir) returns a top-level error.
func ReconcileMotion(ctx context.Context, client MotionClient, localDir string) (ReconcileResult, error) {
	res := ReconcileResult{Errors: map[string]string{}}
	if client == nil {
		return res, errors.New("assetsync: nil client")
	}
	if strings.TrimSpace(localDir) == "" {
		return res, errors.New("assetsync: empty localDir")
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return res, err
	}
	list, err := client.MotionList(ctx)
	if err != nil {
		return res, err
	}
	local := localSHA(localDir)
	for _, it := range list.Items {
		name, ok := safeName(it.Name)
		if !ok {
			res.Errors[it.Name] = "unsafe name"
			continue
		}
		if local[name] == it.SHA256 && it.SHA256 != "" {
			res.Skipped++
			continue
		}
		got, err := client.MotionGet(ctx, name)
		if err != nil {
			res.Errors[name] = err.Error()
			continue
		}
		data, err := base64.StdEncoding.DecodeString(got.JSONBase64)
		if err != nil {
			res.Errors[name] = "decode: " + err.Error()
			continue
		}
		// verify content matches the advertised hash before persisting
		if it.SHA256 != "" {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != it.SHA256 {
				res.Errors[name] = "sha256 mismatch"
				continue
			}
		}
		if err := writeAtomic(filepath.Join(localDir, name+".json"), data); err != nil {
			res.Errors[name] = err.Error()
			continue
		}
		res.Pulled = append(res.Pulled, name)
	}
	return res, nil
}

// ReconcileAllPeers reconciles motion recordings from every connected peer into localDir.
// newClient builds a MotionClient for a peer nodeID (return nil to skip). Best-effort: one peer's
// failure never aborts the others. Returns per-peer results keyed by nodeID.
func ReconcileAllPeers(ctx context.Context, mgr PeerManager, newClient func(nodeID string) MotionClient, localDir string) map[string]ReconcileResult {
	out := map[string]ReconcileResult{}
	if mgr == nil || newClient == nil {
		return out
	}
	for _, p := range mgr.Connections() {
		if p.Status != peerlink.StatusConnected {
			continue
		}
		cl := newClient(p.NodeID)
		if cl == nil {
			continue
		}
		res, err := ReconcileMotion(ctx, cl, localDir)
		if err != nil {
			res.Errors["*"] = err.Error()
		}
		out[p.NodeID] = res
	}
	return out
}

// localSHA maps base recording name → hex sha256 for the *.json files in dir.
func localSHA(dir string) map[string]string {
	out := map[string]string{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		out[strings.TrimSuffix(ent.Name(), ".json")] = hex.EncodeToString(sum[:])
	}
	return out
}

// safeName accepts a base recording name only: non-empty, no path separators, no "..".
func safeName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || filepath.Base(name) != name {
		return "", false
	}
	return name, true
}

// writeAtomic writes data to path via a tmp file + rename (same dir), mirroring vrmotion.Save.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".assetsync-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
