//go:build darwin

package httpapi

import (
	"net"

	"golang.org/x/sys/unix"
)

func acceptedUnixUID(conn net.Conn) (uint32, error) {
	socket, ok := conn.(*net.UnixConn)
	if !ok || socket == nil {
		return 0, problem("unauthorized")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		// LOCAL_PEERCRED is Darwin's getpeereid credential source. Never use
		// the process UID, a socket path owner, a header or a Bearer fallback.
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			socketErr = e
			return
		}
		uid, socketErr = darwinCredentialUID(cred)
	})
	if err != nil {
		return 0, err
	}
	return uid, socketErr
}

func darwinCredentialUID(cred *unix.Xucred) (uint32, error) {
	// XUCRED_VERSION is 0 in Darwin sys/ucred.h. Refuse unknown versions
	// and absent/impossible group data rather than accepting a zero default.
	if cred == nil || cred.Version != 0 || cred.Ngroups < 1 || int(cred.Ngroups) > len(cred.Groups) {
		return 0, problem("unauthorized")
	}
	return cred.Uid, nil
}
