package senderkeeper

import (
	"errors"
	"testing"
)

func fakeSup(f *FakeServer, admit *bool, starts *int) *Supervisor {
	return NewSupervisor(Options{
		Dial: f.Dial,
		Start: func() error {
			*starts++
			f.Revive()
			return nil
		},
		Admit: func(string, int, int) bool { return *admit },
	})
}

// App restart / update: a second Supervisor (new daemon) ADOPTS the keeper's destination - same
// handle, no texture creation, no admission (it costs nothing new and Resolume sees nothing new).
func TestRestartAdoptsWithoutNewSender(t *testing.T) {
	f := NewFakeServer()
	admit, starts := true, 0
	s1 := fakeSup(f, &admit, &starts)
	a, created, err := s1.Ensure("rave-mate link VRC", 3840, 2160)
	if err != nil || !created {
		t.Fatalf("first ensure: %+v created=%v err=%v", a, created, err)
	}
	s1.Close() // daemon exits; keeper keeps the sender

	admit = false // card full now: admission would refuse a NEW sender
	s2 := fakeSup(f, &admit, &starts)
	b, created, err := s2.Ensure("rave-mate link VRC", 1920, 1080) // capped coded size asked
	if err != nil || created {
		t.Fatalf("restart must adopt: created=%v err=%v", created, err)
	}
	if b.Handle != a.Handle || b.W != 3840 || b.H != 2160 || f.Creates != 1 {
		t.Fatalf("adoption changed the destination: %+v vs %+v creates=%d", b, a, f.Creates)
	}
}

// Keeper death: the destination is gone; ONE guarded re-create (through admission) per session, then
// hold - never a sender-churn loop in Resolume.
func TestKeeperDeathOneGuardedRecreate(t *testing.T) {
	f := NewFakeServer()
	admit, starts := true, 0
	s := fakeSup(f, &admit, &starts)
	if _, _, err := s.Ensure("d", 64, 48); err != nil {
		t.Fatal(err)
	}
	f.Kill()
	if _, created, err := s.Ensure("d", 64, 48); err != nil || !created || starts != 1 {
		t.Fatalf("first loss: restart + one re-create, got created=%v err=%v starts=%d", created, err, starts)
	}
	f.Kill()
	if _, _, err := s.Ensure("d", 64, 48); !errors.Is(err, ErrRecreateSpent) {
		t.Fatalf("second loss must hold, got %v", err)
	}
	if f.Creates != 2 {
		t.Fatalf("creates=%d, want 2 (original + one re-create)", f.Creates)
	}
}

// A re-create still goes through admission: a full card holds instead of creating.
func TestRecreateIsAdmissionGated(t *testing.T) {
	f := NewFakeServer()
	admit, starts := true, 0
	s := fakeSup(f, &admit, &starts)
	if _, _, err := s.Ensure("d", 64, 48); err != nil {
		t.Fatal(err)
	}
	f.Kill()
	admit = false
	if _, _, err := s.Ensure("d", 64, 48); !errors.Is(err, ErrHeld) {
		t.Fatalf("want ErrHeld, got %v", err)
	}
	if f.Creates != 1 {
		t.Fatalf("held re-create allocated: creates=%d", f.Creates)
	}
}

// Adopt-only processes (no Start: the media child) never spawn a keeper.
func TestAdoptOnlyNeverStarts(t *testing.T) {
	f := NewFakeServer()
	f.Kill()
	s := NewSupervisor(Options{Dial: f.Dial})
	if _, _, err := s.Ensure("d", 64, 48); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestProtocolClearReleaseAndErrors(t *testing.T) {
	f := NewFakeServer()
	c, err := f.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if h, err := c.Hello(); err != nil || h.Proto != Proto {
		t.Fatalf("hello %+v %v", h, err)
	}
	if _, _, err := c.Ensure("x", 8, 4); err != nil {
		t.Fatal(err)
	}
	if err := c.Clear("x"); err != nil || f.Clears != 1 {
		t.Fatalf("clear: %v clears=%d", err, f.Clears)
	}
	if err := c.Release("x"); err != nil {
		t.Fatal(err)
	}
	if err := c.Release("x"); err == nil {
		t.Fatal("releasing an unknown sender must error")
	}
	if l, err := c.List(); err != nil || len(l) != 0 {
		t.Fatalf("list after release: %+v %v", l, err)
	}
	if _, _, err := c.Ensure("", 8, 4); err == nil {
		t.Fatal("bad name must error")
	}
}
