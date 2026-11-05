package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/relux-works/swarma-board/internal/client"
)

func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func validateDigest(b []byte, sha string, dry bool) error {
	if sha == "" {
		if dry {
			return nil
		}
		return local("import commit requires --sha256 from validation")
	}
	if len(sha) != 64 || sha != digest(b) {
		return client.LocalError("invalid_snapshot", "archive digest does not match --sha256")
	}
	return nil
}
func readArchive(path string) ([]byte, error) {
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, local("import requires a regular archive file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, local("cannot open import archive")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, local("import requires a regular archive file")
	}
	if st.Size() > client.MaxArchive {
		return nil, client.LocalError("payload_too_large", "archive exceeds 64 MiB")
	}
	b, err := io.ReadAll(io.LimitReader(f, client.MaxArchive+1))
	if err != nil {
		return nil, local("cannot read import archive")
	}
	if len(b) > client.MaxArchive {
		return nil, client.LocalError("payload_too_large", "archive exceeds 64 MiB")
	}
	return b, nil
}

// Stage in the destination directory, then link atomically without replacing an
// existing file or symlink. Only complete, synced, mode-0600 archives appear.
func writeArchive(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".swarma-board-export-*")
	if err != nil {
		return outputFailure("cannot stage export archive")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return outputFailure("cannot write export archive")
	}
	if err = f.Sync(); err != nil {
		return outputFailure("cannot sync export archive")
	}
	if err = f.Close(); err != nil {
		return outputFailure("cannot close export archive")
	}
	if err = os.Link(tmp, path); err != nil {
		return outputFailure("cannot publish export archive; destination must be new")
	}
	return nil
}
