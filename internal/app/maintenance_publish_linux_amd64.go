//go:build linux && amd64

package app

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

func renameCheckpointNoReplace(parent *os.File, from, to string) error {
	src, err := syscall.BytePtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := syscall.BytePtrFromString(to)
	if err != nil {
		return err
	}
	// Linux amd64 ABI: __NR_renameat2=316, RENAME_NOREPLACE=1.
	// syscall's frozen amd64 API lacks this constant. ENOSYS/EINVAL refuse;
	// there is deliberately no overwrite-capable os.Rename fallback.
	_, _, errno := syscall.Syscall6(316, parent.Fd(), uintptr(unsafe.Pointer(src)), parent.Fd(), uintptr(unsafe.Pointer(dst)), 1, 0)
	runtime.KeepAlive(parent)
	runtime.KeepAlive(src)
	runtime.KeepAlive(dst)
	if errno != 0 {
		return errno
	}
	return nil
}
