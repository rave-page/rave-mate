package worker

// publish.upload: ship a captured set's audio file to the rave.page media API (initiate →
// chunks → complete → poll until ready). Runs in the `rave-mate worker publish` CHILD, never in
// the daemon: it is high-rate network work over a multi-GB file, exactly the class the repo rule
// puts out-of-process. A stall, a TLS wedge or an OOM here kills only this child; the daemon
// keeps its actWorker free and the job resumes on the next attempt.
//
// Buffer discipline: exactly ONE chunk buffer (<= 10 MiB, the server's per-chunk body cap) plus a
// 1 MiB hash buffer are alive at any time. Nothing accumulates with file size - chunks are read,
// PUT and overwritten in place.
//
// The access token arrives in the job params over the stdio pipe (never argv, never disk) and is
// only ever set as an Authorization header; the api package's doer logs method/path/status only.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/shared/logbus"
)

// Publish job methods.
const (
	MethodPublishUpload = "publish.upload"
)

// Publish upload stages (also the progress event's Stage).
const (
	PublishStageHashing    = "hashing"
	PublishStageUploading  = "uploading"
	PublishStageProcessing = "processing"
	PublishStageDone       = "done"
)

const (
	publishHashBuf = 1 << 20 // hashing read buffer (fixed, independent of file size)
	// publishMaxTimeout bounds one upload attempt; a bigger set resumes on the next attempt
	// rather than holding a child forever.
	publishMaxTimeout    = 6 * time.Hour
	publishDefaultTimout = 2 * time.Hour
	// publishReadyTimeout bounds the server-side scan/promote wait after complete.
	publishReadyTimeout = 45 * time.Minute
)

// PublishUploadIn is the publish.upload job request. Path is worker-side only - it never
// reaches the wire; only its basename ships, as file_name.
type PublishUploadIn struct {
	Path     string `json:"path"`
	MimeType string `json:"mimeType"`
	APIBase  string `json:"apiBase"`
	Token    string `json:"token"`
	// KnownHash is the ledger's hash for this recording's already-published audio. When the
	// file hashes to the same value the bytes are already on the server: the job returns
	// Reused with no upload at all (the metadata-only re-publish path).
	KnownHash     string `json:"knownHash,omitempty"`
	KnownUploadID string `json:"knownUploadId,omitempty"`
	TimeoutSec    int    `json:"timeoutSec,omitempty"`
}

// PublishUploadOut is the terminal publish.upload result.
type PublishUploadOut struct {
	FileHash      string `json:"fileHash"`
	FileSize      int64  `json:"fileSize"`
	MediaUploadID string `json:"mediaUploadId"`
	Status        string `json:"status"`
	Reused        bool   `json:"reused"` // hash matched KnownHash - no bytes sent
	ChunksSent    int    `json:"chunksSent"`
	ChunksSkipped int    `json:"chunksSkipped"` // already on the server (resume)
}

// PublishProgress is one publish.upload progress event ("progress").
type PublishProgress struct {
	Stage      string  `json:"stage"`
	Percent    float64 `json:"percent"` // 0-100 within the stage
	BytesSent  int64   `json:"bytesSent"`
	BytesTotal int64   `json:"bytesTotal"`
	Chunk      int     `json:"chunk"`
	Chunks     int     `json:"chunks"`
	Status     string  `json:"status,omitempty"` // server upload status while processing
}

func publishHandlers() map[string]Handler {
	return map[string]Handler{MethodPublishUpload: publishUpload}
}

// publishUpload hashes the file, opens/resumes the chunked upload, PUTs the missing chunks,
// completes, then polls until the server reports ready.
func publishUpload(params json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	var in PublishUploadIn
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, fmt.Errorf("publish.upload: bad params: %w", err)
	}
	if in.Path == "" || in.APIBase == "" || in.Token == "" {
		return nil, fmt.Errorf("publish.upload: missing path, api base or token")
	}
	fi, err := os.Stat(in.Path)
	if err != nil {
		return nil, fmt.Errorf("publish.upload: %w", err)
	}
	if fi.Size() <= 0 {
		return nil, fmt.Errorf("publish.upload: empty file")
	}

	timeout := time.Duration(in.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = publishDefaultTimout
	}
	if timeout > publishMaxTimeout {
		timeout = publishMaxTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	progress := func(p PublishProgress) {
		if emit != nil {
			emit("progress", p)
		}
	}

	size := fi.Size()
	hash, err := hashFile(ctx, in.Path, size, progress)
	if err != nil {
		return nil, err
	}
	out := PublishUploadOut{FileHash: hash, FileSize: size}

	// Audio unchanged since the last publish: the server already holds these bytes.
	if in.KnownHash != "" && in.KnownHash == hash {
		out.MediaUploadID, out.Reused, out.Status = in.KnownUploadID, true, api.MediaUploadReady
		progress(PublishProgress{Stage: PublishStageDone, Percent: 100, BytesSent: size, BytesTotal: size})
		return json.Marshal(out)
	}

	// The child's bus has no subscribers; it exists because the api doer logs unconditionally.
	client := api.New(in.APIBase, logbus.New(16))
	mime := in.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	init, err := client.InitiateMediaUpload(ctx, in.Token, api.MediaUploadInitiateIn{
		FileName: filepath.Base(in.Path), FileSize: size, MimeType: mime, FileHash: hash,
	})
	if err != nil {
		return nil, fmt.Errorf("initiate: %w", err)
	}
	out.MediaUploadID = init.UploadID

	chunkSize := init.EffectiveChunkSize()
	chunks := int((size + chunkSize - 1) / chunkSize)
	have := receivedSet(ctx, client, in.Token, init)

	sent, skipped, err := putChunks(ctx, client, in.Token, init.UploadID, in.Path, size, chunkSize, chunks, have, progress)
	out.ChunksSent, out.ChunksSkipped = sent, skipped
	if err != nil {
		return nil, err
	}

	if err := client.CompleteMediaUpload(ctx, in.Token, init.UploadID); err != nil {
		return nil, fmt.Errorf("complete: %w", err)
	}
	progress(PublishProgress{Stage: PublishStageProcessing, BytesSent: size, BytesTotal: size, Chunks: chunks})
	st, err := client.AwaitMediaUploadReady(ctx, in.Token, init.UploadID, publishReadyTimeout, func(s api.MediaUploadStatus) {
		progress(PublishProgress{Stage: PublishStageProcessing, BytesSent: size, BytesTotal: size,
			Chunks: chunks, Status: s.Status})
	})
	if err != nil {
		return nil, err
	}
	out.Status = st.Status
	progress(PublishProgress{Stage: PublishStageDone, Percent: 100, BytesSent: size, BytesTotal: size, Chunks: chunks})
	return json.Marshal(out)
}

// hashFile streams the file through sha256 with a fixed 1 MiB buffer, reporting progress.
func hashFile(ctx context.Context, path string, size int64, progress func(PublishProgress)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	buf := make([]byte, publishHashBuf)
	var read int64
	var lastPct float64
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n]) // hash.Hash never errors
			read += int64(n)
			if pct := float64(read) / float64(size) * 100; pct-lastPct >= 5 {
				lastPct = pct
				progress(PublishProgress{Stage: PublishStageHashing, Percent: pct, BytesTotal: size})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", fmt.Errorf("read: %w", rerr)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// receivedSet resolves which chunk indices the server already holds (resume). Falls back to a
// status poll when initiate didn't carry the set; an unknown set means "send everything", which
// is correct-but-slower, never wrong (chunk PUTs are idempotent).
func receivedSet(ctx context.Context, c *api.Client, token string, init api.MediaUploadInit) map[int]bool {
	idx := init.ReceivedChunks
	if len(idx) == 0 && init.Status != "" && init.Status != api.MediaUploadPending {
		if st, err := c.MediaUploadState(ctx, token, init.UploadID); err == nil {
			idx = st.ReceivedChunks
		}
	}
	have := make(map[int]bool, len(idx))
	for _, n := range idx {
		have[n] = true
	}
	return have
}

// putChunks PUTs every chunk the server is missing. One reused buffer, one chunk in flight -
// sequential on purpose: parallel chunks would multiply the buffer AND the uplink pressure for
// no gain on a saturated home connection.
func putChunks(ctx context.Context, c *api.Client, token, uploadID, path string, size, chunkSize int64, chunks int, have map[int]bool, progress func(PublishProgress)) (sent, skipped int, err error) {
	f, oerr := os.Open(path)
	if oerr != nil {
		return 0, 0, fmt.Errorf("open: %w", oerr)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, chunkSize) // the ONLY growing-with-throughput allocation; capped at 10 MiB
	var done int64
	for n := range chunks {
		if ctx.Err() != nil {
			return sent, skipped, ctx.Err()
		}
		off := int64(n) * chunkSize
		want := min(chunkSize, size-off)
		if have[n] {
			skipped++
			done += want
			progress(PublishProgress{Stage: PublishStageUploading, Percent: float64(done) / float64(size) * 100,
				BytesSent: done, BytesTotal: size, Chunk: n + 1, Chunks: chunks})
			continue
		}
		if _, serr := f.Seek(off, io.SeekStart); serr != nil {
			return sent, skipped, fmt.Errorf("seek chunk %d: %w", n, serr)
		}
		if _, rerr := io.ReadFull(f, buf[:want]); rerr != nil {
			return sent, skipped, fmt.Errorf("read chunk %d: %w", n, rerr)
		}
		if perr := c.PutMediaChunk(ctx, token, uploadID, n, buf[:want]); perr != nil {
			return sent, skipped, fmt.Errorf("chunk %d/%d: %w", n+1, chunks, perr)
		}
		sent++
		done += want
		progress(PublishProgress{Stage: PublishStageUploading, Percent: float64(done) / float64(size) * 100,
			BytesSent: done, BytesTotal: size, Chunk: n + 1, Chunks: chunks})
	}
	return sent, skipped, nil
}
