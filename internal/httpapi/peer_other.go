//go:build !linux

package httpapi

import "net"

// SO_PEERCRED support is deliberately Linux-only; other hosts fail closed.
func acceptedUnixUID(net.Conn) (uint32, error) { return 0, problem("unavailable") }
