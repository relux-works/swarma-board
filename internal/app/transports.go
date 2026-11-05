package app

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/relux-works/swarma-board/internal/auth"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/httpapi"
	"github.com/relux-works/swarma-board/internal/query"
	"github.com/relux-works/swarma-board/internal/transfer"
)

func unavailable() error {
	return &contract.Error{Code: "unavailable", Message: "explicit transport dependencies unavailable"}
}

func boundedServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 125 * time.Second, WriteTimeout: 125 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
}

// NewUnixServer requires a current, concurrency-safe protected resolver. It does
// not discover one or open a listener. The actual accepted UnixConn reaches the
// kernel credential hook unchanged. A separate handler must serve remote TLS.
func NewUnixServer(s *Synthetic, resolver auth.ProtectedPeerResolver, commit string) (*http.Server, error) {
	if s == nil || s.Registry == nil || s.Service == nil {
		return nil, unavailable()
	}
	peer, e := auth.NewPeerAuthorizer(resolver)
	if e != nil {
		return nil, e
	}
	h, e := httpapi.New(httpapi.Options{Service: s.Service, Parser: query.New(), Transfer: transfer.New(commit), Authorizer: peer, PeerAuthenticator: peer, StorageReady: s.Registry.Ready, Commit: commit})
	if e != nil {
		return nil, e
	}
	server := boundedServer(h)
	server.ConnContext = httpapi.UnixConnContext
	return server, nil
}

// NewVerifiedTLSServer uses the independently composed Bearer handler. Server
// certificates are explicit; clients must independently verify chain/hostname.
func NewVerifiedTLSServer(h *httpapi.Handler, config *tls.Config) (*http.Server, error) {
	if h == nil || config == nil || len(config.Certificates) == 0 || config.InsecureSkipVerify {
		return nil, unavailable()
	}
	c := config.Clone()
	if c.MinVersion < tls.VersionTLS12 {
		c.MinVersion = tls.VersionTLS12
	}
	if c.MaxVersion != 0 && c.MaxVersion < c.MinVersion {
		return nil, unavailable()
	}
	s := boundedServer(h)
	s.TLSConfig = c
	return s, nil
}

// preflightUnixSocket checks listener prerequisites without creating a socket or
// changing the filesystem. Deployment calls it before provisioning storage.
// It reserves nothing: ListenUnix must repeat it immediately before binding.
func preflightUnixSocket(path string) error {
	if validateUnixEndpoint(path) != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return unavailable()
	}
	dir := filepath.Dir(path)
	st, e := os.Lstat(dir)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return unavailable()
	}
	real, e := filepath.EvalSymlinks(dir)
	if e != nil || real != dir {
		return unavailable()
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return unavailable()
	}
	if _, e = os.Lstat(path); !os.IsNotExist(e) {
		return unavailable()
	}
	return nil
}

// ListenUnix refuses existing sockets, symlinks and nonprivate parent directories.
// The Host supplies an existing absolute directory owned by the service UID.
// No stale socket is deleted, and cleanup removes only the inode we created.
// Shared-UID actors remain indistinguishable; these modes provide no isolation
// against another process using the same OS identity.
func ListenUnix(path string) (net.Listener, error) {
	if e := preflightUnixSocket(path); e != nil {
		return nil, e
	}
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return nil, unavailable()
	}
	l.SetUnlinkOnClose(false)
	inode, e := os.Lstat(path)
	if e != nil {
		l.Close()
		return nil, unavailable()
	}
	wrapped := &ownedSocket{UnixListener: l, path: path, inode: inode}
	if e = os.Chmod(path, 0600); e != nil {
		wrapped.Close()
		return nil, unavailable()
	}
	return wrapped, nil
}

type ownedSocket struct {
	*net.UnixListener
	path  string
	inode os.FileInfo
	once  sync.Once
	err   error
}

func (s *ownedSocket) Close() error {
	s.once.Do(func() {
		s.err = s.UnixListener.Close()
		if st, e := os.Lstat(s.path); e == nil && os.SameFile(st, s.inode) {
			s.err = errors.Join(s.err, os.Remove(s.path))
		}
	})
	return s.err
}

// ServeUntilCanceled stops admission, drains requests, then returns. Storage
// remains owned by the caller until after this function returns.
func ServeUntilCanceled(ctx context.Context, s *http.Server, l net.Listener) error {
	if s == nil || l == nil {
		return unavailable()
	}
	defer s.Close()
	defer l.Close()
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	select {
	case e := <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
		drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		e := s.Shutdown(drain)
		if e != nil {
			s.Close()
		}
		<-done
		return e
	}
}
