package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeMedia is a stand-in for the media-upload API: initiate → chunks → complete → status.
// It asserts the wire contract (checksum header, chunk indices, byte-exact reassembly) so a
// drift on either side fails here rather than against a live set upload.
type fakeMedia struct {
	mu       sync.Mutex
	uploadID string
	chunks   map[int][]byte
	have     []int // pre-seeded "already received" set (resume)
	complete bool
	fileHash string
	chunkSz  int64
	badSum   int // chunk index to reject with a checksum mismatch (-1 = none)
}

func (f *fakeMedia) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/media-upload/initiate", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			FileName string `json:"file_name"`
			FileSize int64  `json:"file_size"`
			MimeType string `json:"mime_type"`
			FileHash string `json:"file_hash"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.fileHash = in.FileHash
		if strings.ContainsAny(in.FileName, `/\`) {
			http.Error(w, "file_name must be a basename", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"upload_id": f.uploadID, "chunk_size": f.chunkSz, "received_chunks": f.have,
		})
	})
	mux.HandleFunc("/media-upload/"+f.uploadID+"/chunks/", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(filepath.Base(r.URL.Path))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		if want := "sha256:" + hex.EncodeToString(sum[:]); r.Header.Get("X-Chunk-Checksum") != want {
			http.Error(w, "checksum mismatch", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if n == f.badSum {
			http.Error(w, "synthetic chunk failure", http.StatusInternalServerError)
			return
		}
		f.chunks[n] = body
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/media-upload/"+f.uploadID+"/complete", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.complete = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/media-upload/"+f.uploadID+"/status", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		st := "pending"
		if f.complete {
			st = "ready"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"upload_id": f.uploadID, "status": st})
	})
	return mux
}

// assembled returns the reassembled bytes in chunk order.
func (f *fakeMedia) assembled() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []byte
	for n := 0; n < len(f.chunks); n++ {
		out = append(out, f.chunks[n]...)
	}
	return out
}

func writeTempAudio(t *testing.T, size int) (path string, sum string) {
	t.Helper()
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i * 7 % 251)
	}
	path = filepath.Join(t.TempDir(), "set.flac")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(buf)
	return path, hex.EncodeToString(h[:])
}

func runPublish(t *testing.T, in PublishUploadIn) (PublishUploadOut, []PublishProgress, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	var events []PublishProgress
	emit := func(event string, data any) {
		if event != "progress" {
			return
		}
		b, _ := json.Marshal(data)
		var p PublishProgress
		if json.Unmarshal(b, &p) == nil {
			events = append(events, p)
		}
	}
	res, err := publishUpload(raw, emit)
	var out PublishUploadOut
	if err == nil {
		if uerr := json.Unmarshal(res, &out); uerr != nil {
			t.Fatalf("unmarshal result: %v", uerr)
		}
	}
	return out, events, err
}

func TestPublishUploadChunksAndCompletes(t *testing.T) {
	path, want := writeTempAudio(t, 12_000)
	f := &fakeMedia{uploadID: "up_1", chunks: map[int][]byte{}, chunkSz: 5000, badSum: -1}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	out, events, err := runPublish(t, PublishUploadIn{
		Path: path, MimeType: "audio/flac", APIBase: srv.URL, Token: "tok",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if out.FileHash != want {
		t.Errorf("hash = %s, want %s", out.FileHash, want)
	}
	if out.FileSize != 12_000 {
		t.Errorf("size = %d, want 12000", out.FileSize)
	}
	if out.ChunksSent != 3 { // 12000 / 5000 → 5000+5000+2000
		t.Errorf("chunksSent = %d, want 3", out.ChunksSent)
	}
	if out.MediaUploadID != "up_1" || out.Status != "ready" || out.Reused {
		t.Errorf("unexpected result %+v", out)
	}
	got := f.assembled()
	gs := sha256.Sum256(got)
	if hex.EncodeToString(gs[:]) != want {
		t.Errorf("server reassembly differs from the source file (%d bytes)", len(got))
	}
	var sawHash, sawUpload, sawProcessing, sawDone bool
	for _, e := range events {
		switch e.Stage {
		case PublishStageHashing:
			sawHash = true
		case PublishStageUploading:
			sawUpload = true
		case PublishStageProcessing:
			sawProcessing = true
		case PublishStageDone:
			sawDone = true
		}
	}
	if !sawHash || !sawUpload || !sawProcessing || !sawDone {
		t.Errorf("missing stages: hash=%v upload=%v processing=%v done=%v", sawHash, sawUpload, sawProcessing, sawDone)
	}
}

func TestPublishUploadResumesReceivedChunks(t *testing.T) {
	path, _ := writeTempAudio(t, 12_000)
	f := &fakeMedia{uploadID: "up_2", chunks: map[int][]byte{}, chunkSz: 5000, have: []int{0, 1}, badSum: -1}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	out, _, err := runPublish(t, PublishUploadIn{Path: path, APIBase: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if out.ChunksSent != 1 || out.ChunksSkipped != 2 {
		t.Errorf("sent=%d skipped=%d, want 1/2", out.ChunksSent, out.ChunksSkipped)
	}
}

func TestPublishUploadReusesUnchangedAudio(t *testing.T) {
	path, want := writeTempAudio(t, 4_000)
	f := &fakeMedia{uploadID: "up_3", chunks: map[int][]byte{}, chunkSz: 5000, badSum: -1}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	out, _, err := runPublish(t, PublishUploadIn{
		Path: path, APIBase: srv.URL, Token: "tok", KnownHash: want, KnownUploadID: "up_prev",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !out.Reused || out.MediaUploadID != "up_prev" || out.ChunksSent != 0 {
		t.Errorf("expected reuse with no bytes sent, got %+v", out)
	}
	if len(f.chunks) != 0 {
		t.Errorf("server received %d chunks on a reuse", len(f.chunks))
	}
}

func TestPublishUploadFailsOnChunkError(t *testing.T) {
	path, _ := writeTempAudio(t, 12_000)
	f := &fakeMedia{uploadID: "up_4", chunks: map[int][]byte{}, chunkSz: 5000, badSum: 1}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	if _, _, err := runPublish(t, PublishUploadIn{Path: path, APIBase: srv.URL, Token: "tok"}); err == nil {
		t.Fatal("expected an error when a chunk PUT fails")
	} else if !strings.Contains(err.Error(), "chunk 2/3") {
		t.Errorf("error should name the failing chunk, got %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.complete {
		t.Error("complete must not be called after a chunk failure")
	}
}

func TestPublishUploadRejectsMissingInputs(t *testing.T) {
	for _, tc := range []struct{ name, params string }{
		{"no path", `{"apiBase":"http://x","token":"t"}`},
		{"no token", `{"path":"x","apiBase":"http://x"}`},
		{"no base", `{"path":"x","token":"t"}`},
	} {
		if _, err := publishUpload(json.RawMessage(tc.params), nil); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
	if _, err := publishUpload(json.RawMessage(fmt.Sprintf(
		`{"path":%q,"apiBase":"http://x","token":"t"}`, filepath.Join(t.TempDir(), "gone.flac"))), nil); err == nil {
		t.Error("missing file: expected an error")
	}
}
