package deployment

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/client"
)

// Config is explicit input, not discovered configuration. ExportDir is the
// separate offline evidence destination, never registry state or a native import
// target. PolicyFile is passed to the static-0600 adapter; this package does
// not parse it or establish its authority. TCP/TLS inputs are represented only
// so a merged CLI configuration cannot silently ignore activation requests.
type Config struct {
	Profile     string
	Endpoint    string
	DataDir     string
	PolicyFile  string
	ExportDir   string
	TCPListen   string
	TLSCertFile string
	TLSKeyFile  string
	RemoteTLS   bool
}

// Validate performs read-only preflight before any constructors that can write.
// It accepts missing data/export/policy paths, since packaging and the adapter
// own creation/readiness. Existing components must not be symlinks. The socket
// directory must already exist; ListenUnix rechecks its owner/private mode and
// refuses occupied paths. This is not a race-proof same-UID security boundary.
func Validate(c Config) (Plan, error) {
	if _, err := Describe(c.Profile); err != nil {
		return Plan{}, err
	}
	if c.RemoteTLS || c.TCPListen != "" || c.TLSCertFile != "" || c.TLSKeyFile != "" {
		return Plan{}, ErrConfig
	}
	t, err := client.NewUnixTransport(c.Endpoint)
	if err != nil {
		return Plan{}, ErrConfig
	}
	t.CloseIdleConnections()
	socket := strings.TrimPrefix(c.Endpoint, "unix://")
	runtimeDir := filepath.Dir(socket)
	resources := []struct {
		path string
		dir  bool
	}{
		{c.DataDir, true}, {runtimeDir, true}, {c.PolicyFile, false}, {c.ExportDir, true},
	}
	for _, r := range resources {
		if !canonical(r.path) || !unaliased(r.path, r.dir) {
			return Plan{}, ErrConfig
		}
	}
	st, err := os.Stat(runtimeDir)
	if err != nil || !st.IsDir() {
		return Plan{}, ErrConfig
	}
	// A client may validate while the service owns a socket. Never accept a
	// symlink or regular file here; ListenUnix still refuses ANY occupied path.
	if st, err = os.Lstat(socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return Plan{}, ErrConfig
		}
	} else if !os.IsNotExist(err) {
		return Plan{}, ErrConfig
	}
	for i, a := range resources {
		for _, b := range resources[i+1:] {
			if contains(a.path, b.path) || contains(b.path, a.path) {
				return Plan{}, ErrConfig
			}
		}
	}
	return Plan{config: c, valid: true}, nil
}

func canonical(p string) bool {
	return utf8.ValidString(p) && filepath.IsAbs(p) && p != string(filepath.Separator) && filepath.Clean(p) == p &&
		!strings.ContainsAny(p, "\\\x00") && !strings.ContainsFunc(p, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
func contains(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+string(filepath.Separator))
}

// Walk existing ancestors without following symlinks. Missing suffixes are
// allowed, permission/other failures refuse. Final file type is adapter-owned,
// but an existing policy path must be a regular file.
func unaliased(p string, directory bool) bool {
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(p, current), current)
	for i, part := range parts {
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return true
		}
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if i < len(parts)-1 || directory {
			if !st.IsDir() {
				return false
			}
		} else if !st.Mode().IsRegular() {
			return false
		}
	}
	return true
}
