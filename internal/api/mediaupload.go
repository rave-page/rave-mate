package api

// Chunked media upload (POST /media-upload/initiate → PUT …/chunks/{n} → POST …/complete →
// GET …/status). Newer than the generated spec, so hand-written over the same redacted-logging
// doer as library.go (method/path/status only - never tokens, never bodies).
//
// Resumability is server-side: initiate is idempotent on (file_hash, user_id), so re-initiating
// the same file returns the SAME upload_id plus whichever chunks already landed. The caller
// (internal/setpublish, running in the publish worker child) skips those and PUTs the rest.
//
// Privacy invariant (playsync/mediasync.go): local file paths never go on the wire. FileName is
// a BASENAME the caller passes deliberately; nothing here reads or forwards a directory.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Media upload lifecycle states (server-driven: virus scan then promote).
const (
	MediaUploadPending  = "pending"
	MediaUploadScanning = "scanning"
	MediaUploadReady    = "ready"
	MediaUploadFailed   = "failed"
)

const (
	// DefaultChunkSize is the fallback when initiate returns no chunk_size.
	DefaultChunkSize = 5 << 20
	// maxChunkBytes is the server's per-chunk body cap - a larger chunk_size is clamped.
	maxChunkBytes = 10 << 20
)

// MediaUploadInitiateIn opens (or resumes) a chunked upload. FileHash is the sha256 hex of the
// whole file and is the resume key.
type MediaUploadInitiateIn struct {
	FileName string `json:"file_name"` // basename only
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
	FileHash string `json:"file_hash"` // sha256 hex
}

// MediaUploadInit is the initiate response: the upload id + the server's chunk size.
type MediaUploadInit struct {
	UploadID  string `json:"upload_id"`
	ChunkSize int64  `json:"chunk_size"`
	// ReceivedChunks is the resume set (0-based indices already stored). Absent on a fresh
	// upload; a server that omits it on resume falls back to the status poll.
	ReceivedChunks []int  `json:"received_chunks"`
	Status         string `json:"status"`
}

// EffectiveChunkSize resolves the server's chunk size into a usable one (default when 0,
// clamped to the per-chunk body cap).
func (m MediaUploadInit) EffectiveChunkSize() int64 {
	n := m.ChunkSize
	if n <= 0 {
		n = DefaultChunkSize
	}
	if n > maxChunkBytes {
		n = maxChunkBytes
	}
	return n
}

// MediaUploadStatus is the status poll: scan/promote state plus the resume set.
type MediaUploadStatus struct {
	UploadID string `json:"upload_id"`
	Status   string `json:"status"` // pending|scanning|ready|failed
	Error    string `json:"error"`
	// ReceivedChunks lists stored chunk indices; ChunksReceived is the count-only form some
	// responses use. Both are read - whichever the server sends.
	ReceivedChunks []int `json:"received_chunks"`
	ChunksReceived int   `json:"chunks_received"`
	ChunkSize      int64 `json:"chunk_size"`
	FileSize       int64 `json:"file_size"`
}

// Done reports whether the upload reached a terminal state.
func (s MediaUploadStatus) Done() bool {
	return s.Status == MediaUploadReady || s.Status == MediaUploadFailed
}

// ChunkChecksum formats a chunk's sha256 for the X-Chunk-Checksum header.
func ChunkChecksum(chunk []byte) string {
	sum := sha256.Sum256(chunk)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// InitiateMediaUpload opens or resumes a chunked upload. Idempotent on (file_hash, user_id):
// re-initiating an interrupted upload returns its existing id.
func (c *Client) InitiateMediaUpload(ctx context.Context, token string, in MediaUploadInitiateIn) (MediaUploadInit, error) {
	if token == "" {
		return MediaUploadInit{}, fmt.Errorf("media upload: unauthenticated")
	}
	if in.FileName == "" || in.FileSize <= 0 || in.FileHash == "" {
		return MediaUploadInit{}, fmt.Errorf("media upload: missing file name, size or hash")
	}
	var out MediaUploadInit
	if err := c.doJSON(ctx, http.MethodPost, c.base+"/media-upload/initiate", token, in, &out, c.bulkDoer); err != nil {
		return MediaUploadInit{}, err
	}
	if out.UploadID == "" {
		return MediaUploadInit{}, fmt.Errorf("media upload: server returned no upload id")
	}
	return out, nil
}

// PutMediaChunk uploads chunk n (0-based) with its sha256 checksum header. Idempotent: a
// re-sent chunk overwrites the stored one byte-for-byte.
func (c *Client) PutMediaChunk(ctx context.Context, token, uploadID string, n int, chunk []byte) error {
	if token == "" {
		return fmt.Errorf("media chunk: unauthenticated")
	}
	if uploadID == "" || n < 0 {
		return fmt.Errorf("media chunk: missing upload id or bad index")
	}
	if len(chunk) == 0 {
		return fmt.Errorf("media chunk: empty chunk %d", n)
	}
	if len(chunk) > maxChunkBytes {
		return fmt.Errorf("media chunk: chunk %d is %d bytes, over the %d cap", n, len(chunk), maxChunkBytes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.base+"/media-upload/"+uploadID+"/chunks/"+strconv.Itoa(n), bytes.NewReader(chunk))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Chunk-Checksum", ChunkChecksum(chunk))
	req.Header.Set("Authorization", "Bearer "+token)
	req.ContentLength = int64(len(chunk))
	resp, err := c.uploadDoer.Do(req)
	if err != nil {
		return err
	}
	return decode(resp, nil)
}

// CompleteMediaUpload finalizes the upload; the server then scans + promotes it.
func (c *Client) CompleteMediaUpload(ctx context.Context, token, uploadID string) error {
	if token == "" {
		return fmt.Errorf("media complete: unauthenticated")
	}
	if uploadID == "" {
		return fmt.Errorf("media complete: missing upload id")
	}
	return c.doJSON(ctx, http.MethodPost, c.base+"/media-upload/"+uploadID+"/complete", token, nil, nil, c.bulkDoer)
}

// MediaUploadState polls one upload's scan/promote state (and its resume set).
func (c *Client) MediaUploadState(ctx context.Context, token, uploadID string) (MediaUploadStatus, error) {
	if token == "" {
		return MediaUploadStatus{}, fmt.Errorf("media status: unauthenticated")
	}
	if uploadID == "" {
		return MediaUploadStatus{}, fmt.Errorf("media status: missing upload id")
	}
	var out MediaUploadStatus
	err := c.doJSON(ctx, http.MethodGet, c.base+"/media-upload/"+uploadID+"/status", token, nil, &out, c.doer)
	return out, err
}

// AwaitMediaUploadReady polls status until ready/failed, ctx cancel, or timeout. onState (may
// be nil) fires on every poll so the caller can surface "processing". Poll interval backs off
// from 1s to 10s - a long scan must not hammer the API.
func (c *Client) AwaitMediaUploadReady(ctx context.Context, token, uploadID string, timeout time.Duration, onState func(MediaUploadStatus)) (MediaUploadStatus, error) {
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	wait := time.Second
	for {
		st, err := c.MediaUploadState(ctx, token, uploadID)
		if err != nil {
			return st, err
		}
		if onState != nil {
			onState(st)
		}
		if st.Status == MediaUploadReady {
			return st, nil
		}
		if st.Status == MediaUploadFailed {
			msg := st.Error
			if msg == "" {
				msg = "upload rejected by the server"
			}
			return st, fmt.Errorf("media upload failed: %s", msg)
		}
		if time.Now().After(deadline) {
			return st, fmt.Errorf("media upload still %q after %s", st.Status, timeout)
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(wait):
		}
		if wait < 10*time.Second {
			wait += time.Second
		}
	}
}
