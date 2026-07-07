package assetsync

// VRM avatar sync. Mirrors the motion path (list → diff by name+sha256 → pull missing → verify hash →
// atomic write) but for large avatar models: files keep their extension (.vrm/.glb/.gltf/.fbx) and transfer
// in CHUNKS streamed to a temp file - whole-file base64 would blow remotectl's 24 MiB frame cap.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
)

// vrmExts are the avatar-model extensions VRM sync replicates (keep in sync with
// remotectl.vrmExts). ".json" = physbones sidecars (vrmdyn) riding along with models.
var vrmExts = []string{".vrm", ".glb", ".gltf", ".fbx", ".json"}

// vrmPullChunk is the per-request chunk size (the server clamps to its own max).
const vrmPullChunk = 8 << 20

// VRMClient is the peer-VRM RPC subset assetsync needs (decoupled for tests). Satisfied by
// *remotectl.Client.
type VRMClient interface {
	VRMList(ctx context.Context) (remotectl.VRMListResult, error)
	VRMGetChunk(ctx context.Context, name string, offset int64, n int) (remotectl.VRMGetChunkResult, error)
}

// ReconcileVRM pulls the peer's avatar models and writes every one whose local copy is missing or whose
// sha256 differs. Large files stream chunk-by-chunk to a temp file, are hash-verified, then renamed.
// Idempotent; per-file failures are collected, only a failed list (or bad dir) returns a top-level error.
func ReconcileVRM(ctx context.Context, client VRMClient, localDir string) (ReconcileResult, error) {
	res := ReconcileResult{Errors: map[string]string{}}
	if client == nil {
		return res, errors.New("assetsync: nil vrm client")
	}
	if strings.TrimSpace(localDir) == "" {
		return res, errors.New("assetsync: empty localDir")
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return res, err
	}
	list, err := client.VRMList(ctx)
	if err != nil {
		return res, err
	}
	local := localSHAExts(localDir, vrmExts)
	for _, it := range list.Items {
		name, ok := safeName(it.Name) // full filename incl. ext; safeName still blocks separators/".."
		if !ok || !hasExt(name, vrmExts) {
			res.Errors[it.Name] = "unsafe name"
			continue
		}
		if local[name] == it.SHA256 && it.SHA256 != "" {
			res.Skipped++
			continue
		}
		if err := pullVRMFile(ctx, client, localDir, name, it.SHA256); err != nil {
			res.Errors[name] = err.Error()
			continue
		}
		res.Pulled = append(res.Pulled, name)
	}
	return res, nil
}

// ReconcileVRMAllPeers reconciles avatar models from every connected peer into localDir. newClient
// builds a VRMClient for a peer nodeID (return nil to skip). Best-effort per-peer.
func ReconcileVRMAllPeers(ctx context.Context, mgr PeerManager, newClient func(nodeID string) VRMClient, localDir string) map[string]ReconcileResult {
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
		res, err := ReconcileVRM(ctx, cl, localDir)
		if err != nil {
			res.Errors["*"] = err.Error()
		}
		out[p.NodeID] = res
	}
	return out
}

// pullVRMFile streams one avatar file over chunked gets into a temp file, verifies the full-file hash
// against wantSHA, then atomically renames it into place. The temp file is removed on any failure.
func pullVRMFile(ctx context.Context, client VRMClient, dir, name, wantSHA string) error {
	tmp, err := os.CreateTemp(dir, ".assetsync-vrm-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }

	h := sha256.New()
	var offset int64
	for {
		chunk, err := client.VRMGetChunk(ctx, name, offset, vrmPullChunk)
		if err != nil {
			cleanup()
			return err
		}
		data, err := base64.StdEncoding.DecodeString(chunk.DataBase64)
		if err != nil {
			cleanup()
			return errors.New("decode: " + err.Error())
		}
		if len(data) > 0 {
			if _, err := tmp.Write(data); err != nil {
				cleanup()
				return err
			}
			h.Write(data)
			offset += int64(len(data))
		}
		if chunk.EOF {
			break
		}
		if len(data) == 0 { // no EOF yet but no bytes → the transfer stalled; bail instead of looping forever
			cleanup()
			return errors.New("empty chunk before EOF")
		}
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if wantSHA != "" && hex.EncodeToString(h.Sum(nil)) != wantSHA {
		_ = os.Remove(tmpName)
		return errors.New("sha256 mismatch")
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// localSHAExts maps full filename → hex sha256 for files in dir whose extension is in exts.
func localSHAExts(dir string, exts []string) map[string]string {
	out := map[string]string{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, ent := range ents {
		if ent.IsDir() || !hasExt(ent.Name(), exts) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		out[ent.Name()] = hex.EncodeToString(sum[:])
	}
	return out
}

func hasExt(name string, exts []string) bool {
	return slices.Contains(exts, strings.ToLower(filepath.Ext(name)))
}
