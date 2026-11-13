//go:build windows && cgo && spout

package mfenc

// Real-GPU gate for rave-mate-keeper (.devnotes/VRAM_FULL_CARD_POLICY.md 1b): the pure-D3D11 Spout
// sender host. Env-gated (RAVE_MATE_SWDEC_GPU=1); keeper exe from RAVE_MATE_KEEPER_EXE or the worktree
// build. Uses an obviously-named test sender and releases it; kills only the keeper it started.
//
//	RAVE_MATE_SWDEC_GPU=1 RAVE_MATE_KEEPER_EXE=...\native\zigkeeper\zig-out\bin\rave-mate-keeper.exe \
//	  go test -tags spout ./internal/mfenc -run TestKeeperGPU -v

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/senderkeeper"
	"rave.page/mate/internal/videoshare"
)

func keeperExe(t *testing.T) string {
	if p := os.Getenv("RAVE_MATE_KEEPER_EXE"); p != "" {
		return p
	}
	p, _ := filepath.Abs(filepath.Join("..", "..", "native", "zigkeeper", "zig-out", "bin", "rave-mate-keeper.exe"))
	if _, err := os.Stat(p); err != nil {
		t.Skip("rave-mate-keeper.exe not built (cd native/zigkeeper && zig build)")
	}
	return p
}

func workingSetMB(pid int) string {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("[math]::Round((Get-Process -Id %d).WorkingSet64/1MB,1)", pid)).Output()
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(out))
}

func TestKeeperGPU(t *testing.T) {
	s := requireSWDecGPU(t)
	t.Setenv("RAVE_MATE_MFDEC_PROBE_BANDS", "1")
	exe := keeperExe(t)
	var procs []*exec.Cmd
	start := func() error {
		c := exec.Command(exe)
		if err := c.Start(); err != nil {
			return err
		}
		procs = append(procs, c)
		return nil
	}
	defer func() {
		for _, c := range procs {
			_ = c.Process.Kill()
			_, _ = c.Process.Wait()
		}
	}()
	sup := senderkeeper.NewSupervisor(senderkeeper.Options{Dial: senderkeeper.DialPipe, Start: start})
	if err := sup.Ping(); err != nil {
		t.Fatalf("keeper not reachable: %v", err)
	}
	if len(procs) == 0 {
		t.Skip("a keeper was already running in this session - not testing against a foreign instance")
	}
	kpid := procs[0].Process.Pid
	t.Logf("keeper pid=%d idle: VRAM=%dMB RAM(ws)=%sMB", kpid, childVRAM(s, uint32(kpid)), workingSetMB(kpid))

	for _, sz := range []struct{ w, h int }{{1920, 1080}, {3840, 2160}} {
		requireFreeVRAM(t, s, 3072)
		name := fmt.Sprintf("rave-mate keeper selftest %dx%d", sz.w, sz.h)
		snd, created, err := sup.Ensure(name, sz.w, sz.h)
		if err != nil || !created || snd.Handle == 0 {
			t.Fatalf("%s: ensure %+v created=%v err=%v", name, snd, created, err)
		}
		time.Sleep(200 * time.Millisecond)
		t.Logf("%s: handle=%#x keeper VRAM=%dMB RAM(ws)=%sMB", name, snd.Handle, childVRAM(s, uint32(kpid)), workingSetMB(kpid))

		// (a) the Spout SDK (receiver side of the registry) sees the keeper's sender exactly.
		h, f, w, hh, ok := videoshare.SenderShare(name)
		if !ok || h != snd.Handle || w != sz.w || hh != sz.h || f != 87 {
			t.Fatalf("Spout registry view: handle=%#x fmt=%d %dx%d ok=%v, want %#x 87 %dx%d", h, f, w, hh, ok, snd.Handle, sz.w, sz.h)
		}
		// (b) the decode child opens the keeper's handle and publishes the band pattern into it.
		aus := encodeAUs(t, 1280, 720, 60, 60)
		d, err := OpenProcDecSession(ProcDecOpts{LUID: 0, InW: 1280, InH: 720, OutW: sz.w, OutH: sz.h, FPS: 60, KbpsHint: 8000,
			Dest: &DecodeDest{Name: name, Resolve: func() (uint64, uint32, int, int, bool) {
				return snd.Handle, 87, sz.w, sz.h, true
			}}})
		if err != nil {
			t.Fatalf("decode into the keeper texture: %v", err)
		}
		for _, au := range aus {
			_ = d.Decode(au.Data, au.PTSNs, au.Keyframe)
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(300 * time.Millisecond)
		st := d.Stats()
		t.Logf("%s: decode child published %d frames into the keeper texture (flags %#x, errors %d)", name, st.DecFrames, st.DecFlags, st.DecErrors)
		if st.DecFrames == 0 || st.DecErrors != 0 {
			dumpChildTail(t, d.child)
			t.Fatalf("decode into the keeper texture failed")
		}
		assertChildBands(t, d.child)
		// (c) a Spout RECEIVER (SpoutLibrary path, the way Resolume reads) reads the picture back.
		img, err := videoshare.GrabSenderFrame(name, sz.w, sz.h)
		if err != nil {
			t.Fatalf("Spout receiver read-back: %v", err)
		}
		top := img.Pix[(sz.h/8)*img.Stride+(sz.w/2)*4:]
		bot := img.Pix[(sz.h*7/8)*img.Stride+(sz.w/2)*4:]
		t.Logf("%s: Spout receiver read-back top r=%d b=%d bottom r=%d b=%d", name, top[0], top[2], bot[0], bot[2])
		if top[0] < 128 || top[2] > 96 || bot[2] < 128 || bot[0] > 96 {
			t.Fatalf("Spout receiver sees the wrong picture")
		}
		d.Close()
		// park = transparent clear (no host blank)
		if err := sup.Clear(name); err != nil {
			t.Fatalf("clear: %v", err)
		}
	}

	// (d) adoption: the daemon-side client goes away (restart/update); a new one adopts - same handle.
	name := "rave-mate keeper selftest 1920x1080"
	first, _, _ := sup.Ensure(name, 1920, 1080)
	sup.Close()
	sup2 := senderkeeper.NewSupervisor(senderkeeper.Options{Dial: senderkeeper.DialPipe, Start: start})
	adopted, created, err := sup2.Ensure(name, 1280, 720) // asks a different size: must adopt unchanged
	if err != nil || created || adopted.Handle != first.Handle || adopted.W != 1920 {
		t.Fatalf("adoption: %+v created=%v err=%v, want handle %#x 1920x1080", adopted, created, err, first.Handle)
	}
	t.Logf("adoption after client restart: same handle %#x, created=false", adopted.Handle)

	// (e) keeper killed: one guarded re-create, then hold.
	_ = procs[0].Process.Kill()
	_, _ = procs[0].Process.Wait()
	time.Sleep(300 * time.Millisecond)
	re, created, err := sup2.Ensure(name, 1920, 1080)
	if err != nil || !created {
		t.Fatalf("after keeper death: want ONE re-create, got created=%v err=%v", created, err)
	}
	t.Logf("keeper death: restarted (pid %d), re-created %s handle %#x", procs[len(procs)-1].Process.Pid, name, re.Handle)
	last := procs[len(procs)-1]
	_ = last.Process.Kill()
	_, _ = last.Process.Wait()
	time.Sleep(300 * time.Millisecond)
	if _, _, err := sup2.Ensure(name, 1920, 1080); !errors.Is(err, senderkeeper.ErrRecreateSpent) {
		t.Fatalf("second keeper death must hold, got %v", err)
	}
	for _, n := range videoshare.ListSenders() {
		if strings.HasPrefix(n, "rave-mate keeper selftest") {
			t.Logf("note: %q still listed after the keeper died (stale Spout name entry; receivers validate the info map)", n)
		}
	}
}
