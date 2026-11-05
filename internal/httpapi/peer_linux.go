//go:build linux

package httpapi

import (
	"net"
	"syscall"
)

func acceptedUnixUID(conn net.Conn) (uint32, error) {
	unix, ok := conn.(*net.UnixConn)
	if !ok || unix == nil {
		return 0, problem("unauthorized")
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e != nil {
			socketErr = e
			return
		}
		if cred == nil {
			socketErr = problem("unauthorized")
			return
		}
		uid = cred.Uid
	})
	if err != nil {
		return 0, err
	}
	return uid, socketErr
}
