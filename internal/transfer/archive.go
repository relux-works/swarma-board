package transfer

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxCompressed int64 = 64 << 20
	MaxExpanded   int64 = 256 << 20
	MaxEntry      int64 = 8 << 20
	MaxEntries          = 10000
	MaxElements         = 10000
)

// The raw block gate prevents archive/tar's transparent PAX/GNU processing
// from hiding special entries, allocations or entry counts. A local PAX header
// may carry exactly one path record, bounded before allocation; archive/tar
// validates it together with the next ordinary header before safePath runs.
func readArchive(ctx context.Context, r io.Reader) (map[string][]byte, []string, string, error) {
	digest := sha256.New()
	cr := &budgetReader{ctx: ctx, r: r, max: MaxCompressed, h: digest}
	br := bufio.NewReader(cr)
	gz, err := gzip.NewReader(br)
	if err != nil {
		return nil, nil, "", invalid("invalid gzip header")
	}
	defer gz.Close()
	gz.Multistream(false)
	er := &budgetReader{ctx: ctx, r: gz, max: MaxExpanded}
	files := map[string][]byte{}
	seen := map[string]bool{}
	dirs := map[string]bool{}
	count := 0
	colonResources := map[string]string{}
	var paxHeader []byte
	for {
		block := make([]byte, 512)
		if _, err = io.ReadFull(er, block); err != nil {
			return nil, nil, "", archiveError(ctx, "truncated or oversized tar", err)
		}
		if allZero(block) {
			if paxHeader != nil {
				return nil, nil, "", invalid("orphan archive path header")
			}
			if _, err = io.ReadFull(er, block); err != nil || !allZero(block) {
				return nil, nil, "", invalid("invalid tar terminator")
			}
			// POSIX tar writers may pad their final record with zero blocks.
			// Count every block; accept no nonzero or partial trailing block.
			for {
				n, e := io.ReadFull(er, block)
				if n == 0 && e == io.EOF {
					break
				}
				if e != nil || !allZero(block) {
					return nil, nil, "", archiveError(ctx, "trailing or invalid archive data", e)
				}
			}
			break
		}
		count++
		if count > MaxEntries {
			return nil, nil, "", invalid("archive entry limit")
		}
		if block[156] != 0 && block[156] != tar.TypeReg && block[156] != tar.TypeDir && block[156] != tar.TypeXHeader {
			return nil, nil, "", invalid("archive special entry refused")
		}
		// Bytes after a NUL in a header's pathname field cannot hide a second name.
		for _, field := range [][]byte{block[:100], block[345:500]} {
			if i := bytes.IndexByte(field, 0); i >= 0 && !allZero(field[i:]) {
				return nil, nil, "", invalid("noncanonical tar pathname")
			}
		}
		if block[156] == tar.TypeXHeader {
			if paxHeader != nil || !allZero(block[157:257]) {
				return nil, nil, "", invalid("invalid archive path header")
			}
			// Inspect the physical size before archive/tar can consume hidden
			// metadata. Non-octal encodings are outside this narrow subset.
			size, e := strconv.ParseInt(strings.Trim(string(block[124:136]), " \x00"), 8, 64)
			if e != nil || size < 0 || size > MaxEntry {
				return nil, nil, "", invalid("archive entry size limit")
			}
			data, e := readArchiveData(ctx, er, size)
			if e != nil {
				return nil, nil, "", e
			}
			if !singlePAXPath(data) {
				return nil, nil, "", invalid("unsupported archive path metadata")
			}
			paxHeader = append(block, data...)
			paxHeader = append(paxHeader, make([]byte, (512-len(data)%512)%512)...)
			continue
		}
		tr := tar.NewReader(io.MultiReader(bytes.NewReader(paxHeader), bytes.NewReader(block)))
		hdr, e := tr.Next()
		if e != nil {
			return nil, nil, "", invalid("invalid tar header")
		}
		isDir := hdr.Typeflag == tar.TypeDir
		pathOnly := paxHeader != nil && len(hdr.PAXRecords) == 1 && hdr.PAXRecords["path"] == hdr.Name
		validPath := safePath(hdr.Name, isDir)
		if !validPath && !isDir && strings.Contains(hdr.Name, ":") {
			if owner, _, ok := legacyResourcePath(hdr.Name); ok {
				validPath = true
				colonResources[hdr.Name] = owner
			}
		}
		if !validPath || hdr.Linkname != "" || len(hdr.PAXRecords) != 0 && !pathOnly || len(hdr.Xattrs) != 0 {
			return nil, nil, "", invalid("unsafe archive path or metadata")
		}
		paxHeader = nil
		key := strings.TrimSuffix(hdr.Name, "/")
		if seen[key] {
			return nil, nil, "", invalid("duplicate archive path")
		}
		seen[key] = true
		if hdr.Size < 0 || hdr.Size > MaxEntry || isDir && hdr.Size != 0 {
			return nil, nil, "", invalid("archive entry size limit")
		}
		if isDir {
			dirs[key] = true
			continue
		}
		data, e := readArchiveData(ctx, er, hdr.Size)
		if e != nil {
			return nil, nil, "", e
		}
		files[hdr.Name] = data
	}
	// Force gzip checksum validation above, then require exactly one member and
	// no compressed tail. bufio implements ReadByte, preventing gzip read-ahead.
	if _, err = br.ReadByte(); err != io.EOF {
		return nil, nil, "", archiveError(ctx, "concatenated gzip or trailing data", err)
	}
	for key := range seen {
		for parent := path.Dir(key); parent != "."; parent = path.Dir(parent) {
			if _, ok := files[parent]; ok {
				return nil, nil, "", invalid("file/directory path collision")
			}
		}
	}
	// Staging a name is not admission: only a closed legacy-v2 manifest and
	// an actual owning element can authorize an inert resource label.
	if len(colonResources) > 0 {
		if !confirmedLegacyResources(files) {
			return nil, nil, "", invalid("unsafe archive path or metadata")
		}
		for _, owner := range colonResources {
			if !hasLegacyResourceOwner(files, owner) {
				return nil, nil, "", invalid("unsafe archive path or metadata")
			}
		}
	}
	directoryNames := make([]string, 0, len(dirs))
	for p := range dirs {
		directoryNames = append(directoryNames, p)
	}
	return files, directoryNames, fmt.Sprintf("%x", digest.Sum(nil)), nil
}

// A single length-framed path record avoids duplicate-key, size, linkpath and
// other metadata overrides. archive/tar still validates the record and headers.
func singlePAXPath(data []byte) bool {
	space := bytes.IndexByte(data, ' ')
	if space < 1 || data[0] == '0' {
		return false
	}
	n, err := strconv.ParseUint(string(data[:space]), 10, 64)
	return err == nil && n == uint64(len(data)) && bytes.HasPrefix(data[space+1:], []byte("path=")) && data[len(data)-1] == '\n'
}

// Every payload, including path metadata and padding, uses the same expanded
// byte budget. Callers check the physical per-entry size before allocation.
func readArchiveData(ctx context.Context, er *budgetReader, size int64) ([]byte, error) {
	padded := (size + 511) / 512 * 512
	if er.n+padded > MaxExpanded {
		return nil, invalid("expanded archive limit")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(er, data); err != nil {
		return nil, archiveError(ctx, "truncated archive entry", err)
	}
	if padded > size {
		pad := make([]byte, int(padded-size))
		if _, err := io.ReadFull(er, pad); err != nil || !allZero(pad) {
			return nil, invalid("invalid tar padding")
		}
	}
	return data, nil
}

type budgetReader struct {
	ctx    context.Context
	r      io.Reader
	n, max int64
	h      hash.Hash
}

func (b *budgetReader) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	remaining := b.max - b.n
	if remaining < 0 {
		return 0, invalid("archive byte limit")
	}
	if int64(len(p)) > remaining+1 {
		p = p[:remaining+1]
	}
	n, e := b.r.Read(p)
	b.n += int64(n)
	if b.h != nil {
		_, _ = b.h.Write(p[:n])
	}
	if b.n > b.max {
		return n, invalid("archive byte limit")
	}
	return n, e
}
func safePath(s string, dir bool) bool {
	if !utf8.ValidString(s) || s == "" || strings.ContainsAny(s, "\\\x00") || strings.HasPrefix(s, "/") || strings.Contains(s, ":") {
		return false
	}
	if dir {
		if !strings.HasSuffix(s, "/") {
			return false
		}
		s = strings.TrimSuffix(s, "/")
	}
	if s == "" || s == "." || path.Clean(s) != s || strings.HasPrefix(s, "../") {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if p == ".." || p == "." || p == "" {
			return false
		}
	}
	return true
}
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
func archiveError(ctx context.Context, message string, _ error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return invalid(message)
}
