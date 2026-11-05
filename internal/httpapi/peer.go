package httpapi

import (
	"context"
	"net"

	"github.com/relux-works/swarma-board/internal/contract"
)

// PeerAuthenticator resolves a kernel UID through protected, current Host policy.
// Only the UID is connection-scoped; principals and grants are never cached here.
type PeerAuthenticator interface {
	AuthenticatePeer(context.Context, uint32) (contract.Principal, error)
	Ready(context.Context) error
}

type peerContextKey struct{}
type peerIdentity struct {
	uid uint32
	ok  bool
}

// UnixConnContext is for http.Server.ConnContext on a dedicated Unix listener.
// It captures kernel peer credentials from the accepted *net.UnixConn
// (SO_PEERCRED on Linux, LOCAL_PEERCRED on Darwin). Unknown/wrapped
// connections and extraction failures install a refusal marker, never a UID zero
// default, address-derived identity or header fallback. Preserve this returned
// context if the integrator composes additional ConnContext hooks.
func UnixConnContext(ctx context.Context, conn net.Conn) context.Context {
	uid, err := acceptedUnixUID(conn)
	return context.WithValue(ctx, peerContextKey{}, peerIdentity{uid: uid, ok: err == nil})
}

func (h *Handler) peerReady(ctx context.Context) error {
	if h.o.PeerAuthenticator != nil {
		return h.o.PeerAuthenticator.Ready(ctx)
	}
	return nil
}
