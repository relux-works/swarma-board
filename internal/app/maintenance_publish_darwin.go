//go:build darwin

package app

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func renameCheckpointNoReplace(parent *os.File, from, to string) error {
	// RENAME_EXCL atomically refuses any existing destination, including an
	// empty directory. Unsupported filesystems/APIs return an error; there is
	// deliberately no overwrite-capable rename or check-then-rename fallback.
	err := unix.RenameatxNp(int(parent.Fd()), from, int(parent.Fd()), to, unix.RENAME_EXCL)
	runtime.KeepAlive(parent)
	return err
}
