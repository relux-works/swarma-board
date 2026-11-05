//go:build unix

package store

import (
	"context"
	"os"
	"syscall"
	"time"

	"github.com/asdine/storm/v3"
	"github.com/relux-works/swarma-board/internal/contract"
	bolt "go.etcd.io/bbolt"
)

// OpenReadOnly opens an existing private database for offline maintenance. A
// shared bbolt file lock excludes writers, with a finite acquisition timeout.
// There is no database creation, Storm initialization, migration or signer call.
// Integration owns stopping the service and protecting its containing directory.
func OpenReadOnly(path string, project contract.Project) (*DB, error) {
	if !ValidProjectID(project.ID) {
		return nil, failure("invalid_request")
	}
	st, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil, failure("not_found")
	}
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() == 0 {
		return nil, failure("unavailable")
	}
	raw, e := bolt.Open(path, 0600, &bolt.Options{
		ReadOnly: true, Timeout: 2 * time.Second,
		// bbolt v1.3.4 normally adds O_CREATE even in ReadOnly mode. Remove
		// that flag and refuse symlinks/replaced inodes at the actual open.
		OpenFile: func(name string, _ int, _ os.FileMode) (*os.File, error) {
			f, e := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if e != nil {
				return nil, e
			}
			actual, e := f.Stat()
			if e != nil || !os.SameFile(st, actual) || !actual.Mode().IsRegular() || actual.Mode().Perm() != 0600 || actual.Size() == 0 {
				f.Close()
				return nil, failure("unavailable")
			}
			return f, nil
		},
	})
	if e != nil {
		return nil, e
	}
	d := &DB{db: &storm.DB{Bolt: raw}, project: project}
	if e = d.Ready(context.Background()); e != nil {
		raw.Close()
		return nil, e
	}
	return d, nil
}
