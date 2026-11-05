//go:build unix

package store

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockDirectory bounds acquisition to two seconds and never follows a lock symlink.
func LockDirectory(dir string) (*DirectoryLock, error) {
	path := filepath.Join(dir, ".owner.lock")
	f, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		f.Close()
		return nil, failure("unavailable")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return &DirectoryLock{file: f}, nil
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			f.Close()
			return nil, e
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, failure("unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// DirectoryLock is an exclusive process-level guard shared by registry and restore.
type DirectoryLock struct{ file *os.File }

func (l *DirectoryLock) Close() error {
	e := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	c := l.file.Close()
	if e != nil {
		return e
	}
	return c
}
