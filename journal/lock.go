package journal

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrLocked means another gantline process held the lock for the whole wait.
var ErrLocked = errors.New("journal: pane write lock busy")

// LockWrites takes key's write lock, an flock on <PaneDir>/write.lock, and
// returns its release. Every gantline process that types into a pane holds
// it from the first key to the last Enter, so two writers (two queued
// commands, or a queued command and a `say` from another pane) never
// interleave keystrokes. The kernel drops the lock if the process dies.
func LockWrites(key string, wait time.Duration) (release func(), err error) {
	return Lock(key, "write.lock", wait)
}

// Lock takes the flock <PaneDir>/<name>, waiting up to wait (zero: try
// once), and returns its release, or ErrLocked.
func Lock(key, name string, wait time.Duration) (release func(), err error) {
	if key == "" {
		return nil, errors.New("journal: no key")
	}
	dir := PaneDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, ErrLocked
		}
		time.Sleep(50 * time.Millisecond)
	}
}
