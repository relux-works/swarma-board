package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// This origin exists only inside HTTP messages carried on the selected Unix
// socket. No DNS lookup or TCP connection is ever made for it.
const unixOrigin = "http://swarma-board.local"

// UnixTransport is a socket-bound HTTP transport for explicit injection into
// Options. Its internals are private so composition cannot add proxies, bearer
// injection, alternate protocols, TCP fallback or insecure TLS hooks.
// A shared OS UID proves transport only, never per-agent identity isolation.
type UnixTransport struct {
	endpoint  string
	transport *http.Transport
}

// NewUnixTransport creates a transport without connecting, starting a service,
// discovering configuration or resolving credentials. It accepts only canonical
// unix:///absolute/socket/path endpoints without URL escapes or whitespace.
func NewUnixTransport(endpoint string) (*UnixTransport, error) {
	socket, err := unixSocketPath(endpoint)
	if err != nil {
		return nil, err
	}
	tr := newHTTPTransport()
	tr.ForceAttemptHTTP2 = false
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}
	return &UnixTransport{endpoint: endpoint, transport: tr}, nil
}

func unixSocketPath(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || !strings.HasPrefix(endpoint, "unix:///") ||
		u.Scheme != "unix" || u.Host != "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" ||
		u.RawPath != "" || !utf8.ValidString(endpoint) || strings.ContainsAny(endpoint, "%?#\\") ||
		strings.ContainsFunc(endpoint, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) ||
		!path.IsAbs(u.Path) || u.Path == "/" || path.Clean(u.Path) != u.Path || endpoint != "unix://"+u.Path {
		return "", LocalError("invalid_request", "Unix endpoint requires a canonical absolute socket path")
	}
	return u.Path, nil
}

func (t *UnixTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil || t.transport == nil || req == nil || req.URL == nil ||
		req.URL.Scheme != "http" || req.URL.Host != "swarma-board.local" || req.URL.User != nil ||
		(req.Host != "" && req.Host != "swarma-board.local") || hasAuthorization(req.Header) {
		return nil, LocalError("invalid_request", "Unix transport refused request origin or authorization headers")
	}
	return t.transport.RoundTrip(req)
}

func (t *UnixTransport) CloseIdleConnections() {
	if t != nil && t.transport != nil {
		t.transport.CloseIdleConnections()
	}
}

func hasAuthorization(headers http.Header) bool {
	for name := range headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") {
			return true
		}
	}
	return false
}

func newHTTPTransport() *http.Transport {
	return &http.Transport{
		DialContext:       (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 16, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: 305 * time.Second, MaxResponseHeaderBytes: 32 << 10,
	}
}

// A dial refusal is service-dead evidence, never permission to spawn or switch
// endpoints. Other failures retain the existing unknown-write-outcome contract.
func requestError(err error, write bool) *Error {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		message := "service unavailable at selected endpoint"
		if write {
			message += "; write outcome unknown; read and reconcile before another write"
		}
		return failure("unavailable", message, "", 0)
	}
	return transportError(write)
}
