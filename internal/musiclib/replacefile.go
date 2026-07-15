package musiclib

import (
	"errors"
	"os"
	"time"
)

// replaceFile renames tmp over dst atomically, retrying a transient Windows sharing violation.
//
// Go opens files WITHOUT FILE_SHARE_DELETE, so while ANY process holds dst open for reading,
// MoveFileEx (os.Rename) fails with ErrPermission - and the collection is watched by our OWN
// nmlsrc source, which re-parses collection.nml the instant this very write lands (plus antivirus
// / OneDrive / Dropbox routinely scan a just-written file). Every such holder releases within
// milliseconds, so a short bounded retry turns the misleading "file is locked - close your DJ
// software" failure into a clean write. Also clears a read-only bit on the target (the other
// Windows ErrPermission cause) so an accidentally read-only collection still writes. The temp is
// already fully written + fsync'd by the caller, so atomicity is preserved: exactly one rename wins.
//
// Non-ErrPermission errors return immediately (a real, non-transient failure).
func replaceFile(tmp, dst string) error {
	const tries = 16 // ~4s total: backoff 50,100,…,300ms (capped)
	var origMode os.FileMode
	if fi, e := os.Stat(dst); e == nil {
		origMode = fi.Mode().Perm()
	}
	roCleared := false // we stripped the read-only bit to let the replace through - restore it after
	var err error
	for i := 0; i < tries; i++ {
		if err = os.Rename(tmp, dst); err == nil {
			if roCleared { // dst is now the new file - re-apply the caller's read-only protection
				_ = os.Chmod(dst, origMode)
			}
			return nil
		}
		if !errors.Is(err, os.ErrPermission) {
			return err
		}
		// read-only target? clear it once (Windows maps the owner-write bit to FILE_ATTRIBUTE_READONLY).
		if origMode != 0 && origMode&0o200 == 0 && !roCleared {
			_ = os.Chmod(dst, origMode|0o200)
			roCleared = true
			continue // retry immediately - the bit, not a lock, blocked this attempt
		}
		if i < tries-1 { // no point sleeping after the final attempt
			d := time.Duration(50*(i+1)) * time.Millisecond
			if d > 300*time.Millisecond {
				d = 300 * time.Millisecond
			}
			time.Sleep(d)
		}
	}
	if roCleared { // gave up - the original file survives; restore the read-only bit we cleared
		_ = os.Chmod(dst, origMode)
	}
	return err
}
