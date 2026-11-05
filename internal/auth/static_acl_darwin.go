//go:build darwin

package auth

import (
	"encoding/binary"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Darwin ACLs can grant access beyond mode 0600. Ask the opened file for its
// extended security attribute, accepting only the kernel's absent-ACL encoding.
// No returned-attributes or PACK_INVAL flags: unsupported attributes must fail,
// rather than look like an absent ACL. REPORT_FULLSIZE detects truncation.
// The fixed response is uint32 length plus attrreference {int32 offset, uint32
// length}; any ACL (even a deny-only/empty ACL), unknown layout or error refuses.
func staticPolicyNoACL(fd int) error {
	attrs := struct {
		count, reserved                       uint16
		common, volume, directory, file, fork uint32
	}{count: unix.ATTR_BIT_MAP_COUNT, common: unix.ATTR_CMN_EXTENDED_SECURITY}
	var buf [12]byte
	// x/sys has no fgetattrlist wrapper. This syscall number and attrlist ABI
	// are shared by Darwin amd64/arm64. Native execution is a release gate.
	_, _, errno := syscall.Syscall6(unix.SYS_FGETATTRLIST, uintptr(fd),
		uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), unix.FSOPT_REPORT_FULLSIZE, 0)
	runtime.KeepAlive(&attrs)
	runtime.KeepAlive(&buf)
	if errno != 0 {
		return errno
	}
	if binary.LittleEndian.Uint32(buf[:4]) != 12 ||
		binary.LittleEndian.Uint32(buf[4:8]) != 8 ||
		binary.LittleEndian.Uint32(buf[8:12]) != 0 {
		return refuse("unavailable")
	}
	return nil
}
