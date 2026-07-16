// Package matepreset is the file-backed preset round-trip store behind the editor-bridge
// /v1/presets routes (matebridge.Presets). The world's per-module DTO travels VERBATIM as an opaque
// payload (json.RawMessage) - matepreset never interprets it, only versions + persists it. Each Put
// stamps a persisted, strictly-increasing seq (shared with the gist SEQ-GATE via gistseq) so the
// editor can poll "presets changed since seq N" and a preset republished to a gist carries a valid
// runtime seq. Layout: <dir>/<kind>/<id>.json, one file per preset.
package matepreset

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"rave.page/mate/internal/matebridge"
)

// SeqCounter issues persisted, strictly-increasing per-kind seq numbers (satisfied by
// *gistseq.Counter). Nil is tolerated: Put falls back to an in-memory monotonic per-kind seq (fine
// for edit-time provenance; the runtime SEQ-GATE only matters once a preset is gist-published).
type SeqCounter interface {
	Next(module string) int64
}

// validKind is the frozen preset-kind set (matebridge preset constants). An unknown kind is a 400.
var validKind = map[string]bool{
	matebridge.PresetBackdrop:    true,
	matebridge.PresetFoliage:     true,
	matebridge.PresetStageRig:    true,
	matebridge.PresetCameraPath:  true,
	matebridge.PresetDMXMap:      true,
	matebridge.PresetFixtureType: true,
}

// Store persists presets under dir. Safe for concurrent editor requests.
type Store struct {
	dir string
	seq SeqCounter

	mu      sync.Mutex
	fallSeq map[string]int64 // in-memory seq when seq==nil
}

// NewStore builds a store rooted at dir (created lazily on first write). seq may be nil.
func NewStore(dir string, seq SeqCounter) *Store {
	return &Store{dir: dir, seq: seq, fallSeq: map[string]int64{}}
}

// Available reports the store is usable (a dir is configured). Satisfies matebridge.Availabler so
// /v1/health advertises presets only when wired.
func (s *Store) Available() bool { return s.dir != "" }

// List returns every preset of kind with seq > sinceSeq, ascending by seq (the editor's poll shape).
func (s *Store) List(_ context.Context, kind string, sinceSeq int64) ([]matebridge.PresetEnvelope, error) {
	if !validKind[kind] {
		return nil, fmt.Errorf("%w: unknown preset kind %q", matebridge.ErrBadRequest, kind)
	}
	kd := filepath.Join(s.dir, kind)
	ents, err := os.ReadDir(kd)
	if err != nil {
		if os.IsNotExist(err) {
			return []matebridge.PresetEnvelope{}, nil // no presets of this kind yet
		}
		return nil, err
	}
	out := make([]matebridge.PresetEnvelope, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p, err := s.readFile(filepath.Join(kd, e.Name()))
		if err != nil {
			continue // skip a corrupt/partial file rather than fail the whole list
		}
		if p.Seq > sinceSeq {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// Get returns one preset (nil, nil when absent -> the handler answers 404).
func (s *Store) Get(_ context.Context, kind, id string) (*matebridge.PresetEnvelope, error) {
	if !validKind[kind] {
		return nil, fmt.Errorf("%w: unknown preset kind %q", matebridge.ErrBadRequest, kind)
	}
	fp, err := s.path(kind, id)
	if err != nil {
		return nil, err
	}
	p, err := s.readFile(fp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// Put persists a Unity-authored preset and returns the assigned seq. It normalizes envelope
// metadata (schema/kind/id/contractVersion) so the stored file is canonical regardless of what the
// editor sent; the opaque Payload is written verbatim.
func (s *Store) Put(_ context.Context, kind, id string, p matebridge.PresetEnvelope) (int64, error) {
	if !validKind[kind] {
		return 0, fmt.Errorf("%w: unknown preset kind %q", matebridge.ErrBadRequest, kind)
	}
	if strings.TrimSpace(id) == "" {
		return 0, fmt.Errorf("%w: empty preset id", matebridge.ErrBadRequest)
	}
	fp, err := s.path(kind, id)
	if err != nil {
		return 0, err
	}
	seq := s.nextSeq(kind)
	p.Schema = matebridge.PresetSchema
	p.ContractVersion = matebridge.ContractVersion
	p.Kind = kind
	p.ID = id
	p.Seq = seq
	if len(p.Payload) == 0 {
		p.Payload = json.RawMessage("null")
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		return 0, err
	}
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, fp); err != nil {
		return 0, err
	}
	return seq, nil
}

func (s *Store) nextSeq(kind string) int64 {
	key := "preset:" + kind
	if s.seq != nil {
		return s.seq.Next(key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallSeq[key]++
	return s.fallSeq[key]
}

func (s *Store) readFile(fp string) (*matebridge.PresetEnvelope, error) {
	raw, err := os.ReadFile(fp)
	if err != nil {
		return nil, err
	}
	var p matebridge.PresetEnvelope
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// path builds the on-disk path for (kind,id), rejecting an id that would escape the kind dir
// (path traversal guard: ids are editor-supplied).
func (s *Store) path(kind, id string) (string, error) {
	if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", fmt.Errorf("%w: illegal preset id %q", matebridge.ErrBadRequest, id)
	}
	clean := filepath.Join(s.dir, kind, id+".json")
	kd := filepath.Clean(filepath.Join(s.dir, kind))
	if !strings.HasPrefix(filepath.Clean(clean), kd+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: illegal preset id %q", matebridge.ErrBadRequest, id)
	}
	return clean, nil
}
