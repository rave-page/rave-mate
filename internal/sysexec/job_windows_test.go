//go:build windows

package sysexec

import "testing"

// The CPU discipline per class is the whole point of the split: realtime uncapped, batch at the
// original 10% bucket, live media bounded but generous.
func TestCPURateForClass(t *testing.T) {
	for _, tc := range []struct {
		class JobClass
		want  uint32
	}{
		{JobRealtime, 0},
		{JobBatch, 1000},
		{JobMedia, 7000},
		{JobClass(99), 0}, // unknown class must degrade to uncapped kill-on-close, never to a cap
	} {
		if got := cpuRateFor(tc.class); got != tc.want {
			t.Errorf("cpuRateFor(%d) = %d, want %d", tc.class, got, tc.want)
		}
	}
}

// Each class gets its OWN job handle (a shared handle would re-apply one cap to every class).
func TestEnsureJobPerClassHandles(t *testing.T) {
	for _, c := range []JobClass{JobRealtime, JobBatch, JobMedia} {
		ensureJob(c)
		if jobs[c].h == 0 {
			t.Fatalf("class %d: no job handle created", c)
		}
	}
	if jobs[JobRealtime].h == jobs[JobBatch].h || jobs[JobBatch].h == jobs[JobMedia].h ||
		jobs[JobRealtime].h == jobs[JobMedia].h {
		t.Fatalf("job handles must differ per class: %v %v %v",
			jobs[JobRealtime].h, jobs[JobBatch].h, jobs[JobMedia].h)
	}
	// Out-of-range class must not index out of bounds / panic.
	ensureJob(JobClass(42))
}

// The legacy bool API keeps its meaning: false = realtime job, true = the 10%-capped batch job.
func TestAssignToJobBoolMapping(t *testing.T) {
	AssignToJob(nil, false) // nil process is a no-op, never a panic
	AssignToJob(nil, true)
	AssignToJobClass(nil, JobMedia)
	AssignToJobMem(nil, 64)
	if cpuRateFor(JobBatch) == 0 {
		t.Fatal("background=true must still map to a CPU-capped job")
	}
}
