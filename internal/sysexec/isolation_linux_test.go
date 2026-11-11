//go:build linux

package sysexec

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Hide must put a child in its own process group and arm Pdeathsig, the two isolation guarantees
// KillTree + the daemon-death backstop rely on.
func TestHideSetsIsolation(t *testing.T) {
	c := exec.Command("true")
	Hide(c)
	if c.SysProcAttr == nil || !c.SysProcAttr.Setpgid {
		t.Fatal("Hide did not set Setpgid")
	}
	if c.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("Pdeathsig = %v, want SIGKILL", c.SysProcAttr.Pdeathsig)
	}
}

// KillTree must reap the whole process GROUP, not just the direct child: a grandchild (the real
// encoder behind a PATH ffmpeg launcher shim) must die too. A bare p.Kill() would leave the
// grandchild reparented to init and alive - this test fails in that case.
func TestKillTreeGroupReapsGrandchild(t *testing.T) {
	// sh stays as the group leader; `sleep 300` is a grandchild in the same group. Print its pid.
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 & echo $!; wait")
	Hide(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	gc, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		KillTree(cmd.Process)
		t.Fatalf("could not read grandchild pid: %q (%v)", line, err)
	}
	if err := unix.Kill(gc, 0); err != nil {
		KillTree(cmd.Process)
		t.Fatalf("grandchild %d not alive before kill: %v", gc, err)
	}
	KillTree(cmd.Process)
	_ = cmd.Wait()
	// The group SIGKILL is immediate; poll briefly for the grandchild to disappear.
	alive := true
	for i := 0; i < 50; i++ {
		if err := unix.Kill(gc, 0); err == unix.ESRCH {
			alive = false
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if alive {
		_ = unix.Kill(gc, syscall.SIGKILL) // cleanup if the test is about to fail
		t.Fatalf("grandchild %d survived KillTree - group kill did not reap the tree", gc)
	}
}

// AssignToJobClass must nice the child per its class (JobBatch -> 19).
func TestAssignToJobClassNice(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	Hide(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { KillTree(cmd.Process); _ = cmd.Wait() }()
	AssignToJobClass(cmd.Process, JobBatch)
	if n := procNice(t, cmd.Process.Pid); n != 19 {
		t.Fatalf("JobBatch child nice = %d, want 19", n)
	}
}

// Memory-cgroup helpers must never panic and must degrade cleanly where cgroup v2 isn't delegated
// (the usual CI/container case) - returning a well-formed result, not crashing.
func TestCgroupHelpersDegrade(t *testing.T) {
	base, ok := cgroupV2Self()
	t.Logf("cgroupV2Self: base=%q ok=%v", base, ok)
	gotBase, usable := cgroupMemReady()
	t.Logf("cgroupMemReady: base=%q usable=%v", gotBase, usable)
	// A no-cgroup/undelegated host must report unusable, not error out. applyMemCgroup on a bogus
	// pid must be a safe no-op.
	applyMemCgroup(1<<30, 64*1024*1024)
}

// procNice reads a pid's scheduling nice from /proc/<pid>/stat (encoding-free, unlike the raw
// getpriority syscall's 20-nice return). nice is the 17th field after the ")"-terminated comm.
func procNice(t *testing.T, pid int) int {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	rp := strings.LastIndexByte(s, ')')
	if rp < 0 {
		t.Fatalf("bad /proc stat: %q", s)
	}
	f := strings.Fields(s[rp+1:]) // [state ppid pgrp session tty tpgid flags minflt.. priority nice ..]
	if len(f) < 17 {
		t.Fatalf("short /proc stat fields: %v", f)
	}
	n, err := strconv.Atoi(f[16]) // index: state=0 ... priority=15, nice=16
	if err != nil {
		t.Fatalf("nice parse: %v", err)
	}
	return n
}
