// Package store is rave-mate's local persistence: a single embedded bbolt KV file. Holds
// the analysis cache (waveform/tags/fingerprint keyed by path+mtime so it survives restarts
// and invalidates when a file changes) and JSON buckets for automations / scheduled jobs /
// run history. bbolt is pure-Go, single-file, crash-safe (etcd's engine) - no cgo, no server.
package store

import (
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Buckets. analysis = path-keyed blobs (mtime-tagged); the rest are id-keyed JSON.
const (
	bucketAnalysis    = "analysis"
	BucketAutomations = "automations"
	BucketSchedules   = "schedules"
	BucketJobs        = "jobs"
	BucketRuns        = "runs"
	BucketRecordings  = "recordings"       // recorded live-session tracklists
	BucketIdentity    = "identity"         // this node's long-term keypair (LAN peer link)
	BucketPeers       = "peers"            // remembered LAN peers (node_id-keyed)
	BucketStudioFav   = "studio_favorites" // Local Studio browser favorites (id-keyed)
	BucketStudioPre   = "studio_presets"   // Local Studio custom transcode presets (id-keyed)
	BucketStudioRec   = "studio_recents"   // Local Studio recent paths (path-keyed)
	BucketAuthz       = "authz"            // access gate: TOTP enrolment + trusted-session tokens (sealed)
)

var buckets = []string{bucketAnalysis, BucketAutomations, BucketSchedules, BucketJobs, BucketRuns, BucketRecordings, BucketIdentity, BucketPeers, BucketStudioFav, BucketStudioPre, BucketStudioRec, BucketAuthz}

// Analysis kinds (path-keyed cache namespaces).
const (
	KindWaveform    = "wave"
	KindPeaks       = "peaks" // waveform peak buckets (uint8 per bucket; see worker probe.peaks)
	KindTags        = "tags"
	KindFingerprint = "fp"
	KindLoudness    = "lufs"     // EBU R128 measurement (transcode.Measurement JSON)
	KindLoudnessTL  = "loudtl"   // EBU R128 momentary timeline (worker LoudTimeline JSON)
	KindAlign       = "alignoff" // cross-recording time offset (setalign result JSON; path = "a\x1fb")
	// KindSetTrackLinks caches a recording's resolved per-track library paths ([]string JSON,
	// key = recording ID). NOT path-keyed: the mtime slot carries the libdb LibraryVersion() so
	// any library/collection change invalidates it and it re-resolves off-thread (never in render).
	KindSetTrackLinks = "trklinks"
	KindEnvelope      = "envelope" // RMS envelope buckets (worker probe.envelope; input to setalign)
	KindStreams       = "streams"  // ffprobe stream/format info (transcode.SourceInfo JSON)
	KindSilence       = "silence"  // leading/trailing silence probe (worker transcode.silence JSON)
	KindFileHash      = "filehash" // sha256 of a file's bytes (peer avatar/motion listing; mtime-keyed)
)

// Store wraps the bbolt DB. A nil *Store is valid - every method is a safe no-op so callers
// don't need to branch when persistence is unavailable.
type Store struct {
	db *bolt.DB
}

// Open opens (creating if needed) the DB at path and ensures the buckets exist.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range buckets {
			if _, e := tx.CreateBucketIfNotExists([]byte(b)); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close flushes + closes the DB.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func akey(kind, path string) []byte { return []byte(kind + "\x00" + filepath.Clean(path)) }

// GetAnalysis returns cached data for (kind, path) iff present AND its stored mtime matches
// (so an edited file re-analyses). data is a copy safe to retain past the transaction.
func (s *Store) GetAnalysis(kind, path string, mtime int64) ([]byte, bool) {
	if s == nil || s.db == nil {
		return nil, false
	}
	var out []byte
	var ok bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(bucketAnalysis)).Get(akey(kind, path))
		if len(v) < 8 || int64(binary.BigEndian.Uint64(v[:8])) != mtime {
			return nil
		}
		out = append([]byte(nil), v[8:]...)
		ok = true
		return nil
	})
	return out, ok
}

// analysisKinds: the plain per-path cache namespaces RetagAnalyses sweeps (KindAlign is
// composite-keyed + summed-mtime - excluded).
// Audio-bytes-derived kinds: a self-inflicted tag rewrite leaves the audio unchanged, so these
// stay valid and are re-keyed to the new mtime (KindFileHash covers whole-file bytes incl. tags,
// so it is deliberately excluded - a tag write must invalidate it).
var analysisKinds = []string{KindWaveform, KindPeaks, KindTags, KindFingerprint, KindLoudness, KindLoudnessTL, KindEnvelope, KindStreams, KindSilence}

// RetagAnalyses re-keys path's analysis blobs from oldMtime to newMtime. For self-inflicted
// tag rewrites: audio bytes are unchanged, so peaks/loudness/fingerprint caches stay valid -
// without this every drop tag write forced a full re-analysis next session. Only entries
// currently valid (stored mtime == oldMtime) move; stale blobs stay stale.
func (s *Store) RetagAnalyses(path string, oldMtime, newMtime int64) {
	if s == nil || s.db == nil || oldMtime == newMtime {
		return
	}
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketAnalysis))
		for _, kind := range analysisKinds {
			k := akey(kind, path)
			v := b.Get(k)
			if len(v) < 8 || int64(binary.BigEndian.Uint64(v[:8])) != oldMtime {
				continue
			}
			nv := append([]byte(nil), v...)
			binary.BigEndian.PutUint64(nv[:8], uint64(newMtime))
			if err := b.Put(k, nv); err != nil {
				return err
			}
		}
		return nil
	})
}

// PutAnalysis stores data for (kind, path) tagged with mtime.
func (s *Store) PutAnalysis(kind, path string, mtime int64, data []byte) {
	if s == nil || s.db == nil {
		return
	}
	buf := make([]byte, 8+len(data))
	binary.BigEndian.PutUint64(buf[:8], uint64(mtime))
	copy(buf[8:], data)
	_ = s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucketAnalysis)).Put(akey(kind, path), buf)
	})
}

// ── id-keyed JSON buckets (automations / jobs / runs) ────────────────────────

// PutJSON stores v as JSON under bucket/key.
func (s *Store) PutJSON(bucket, key string, v any) error {
	if s == nil || s.db == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucket)).Put([]byte(key), raw)
	})
}

// GetJSON unmarshals bucket/key into v. Returns false if the key is absent.
func (s *Store) GetJSON(bucket, key string, v any) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	var raw []byte
	if err := s.db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket([]byte(bucket)).Get([]byte(key)); b != nil {
			raw = append([]byte(nil), b...)
		}
		return nil
	}); err != nil {
		return false, err
	}
	if raw == nil {
		return false, nil
	}
	return true, json.Unmarshal(raw, v)
}

// ListJSON returns every value in bucket as raw JSON, in key order.
func (s *Store) ListJSON(bucket string) ([][]byte, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var out [][]byte
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucket)).ForEach(func(_, v []byte) error {
			out = append(out, append([]byte(nil), v...))
			return nil
		})
	})
	return out, err
}

// Delete removes bucket/key.
func (s *Store) Delete(bucket, key string) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucket)).Delete([]byte(key))
	})
}
