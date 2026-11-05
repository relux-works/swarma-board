//go:build !linux && !darwin

package httpapi

import "net"

// Hosts without a qualified kernel peer credential implementation fail closed.
func acceptedUnixUID(net.Conn) (uint32, error) { return 0, problem("unavailable") }
