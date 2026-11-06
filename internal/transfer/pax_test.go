package transfer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func paxPathRecord(name string) []byte {
	body := " path=" + name + "\n"
	length := len(body) + 1
	for {
		record := fmt.Sprint(length) + body
		if len(record) == length {
			return []byte(record)
		}
		length = len(record)
	}
}

func paxHeader(t *testing.T, size int64) []byte {
	t.Helper()
	var header bytes.Buffer
	w := tar.NewWriter(&header)
	if err := w.WriteHeader(&tar.Header{Name: "PaxHeaders/path", Typeflag: tar.TypeReg, Size: size, Mode: 0600, Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	b := header.Bytes()
	b[156] = tar.TypeXHeader
	for i := 148; i < 156; i++ {
		b[i] = ' '
	}
	sum := 0
	for _, c := range b {
		sum += int(c)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

func gzipTar(t *testing.T, raw []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	g := gzip.NewWriter(&compressed)
	if _, err := g.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func paxRaw(t *testing.T, records []byte, entries ...entry) []byte {
	t.Helper()
	var raw bytes.Buffer
	raw.Write(paxHeader(t, int64(len(records))))
	raw.Write(records)
	raw.Write(make([]byte, (512-len(records)%512)%512))
	w := tar.NewWriter(&raw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Size: e.size, Mode: 0600, Format: tar.FormatUSTAR}
		if e.typ == tar.TypeSymlink || e.typ == tar.TypeLink {
			h.Linkname = "target"
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func archiveRefused(t *testing.T, archive []byte) {
	t.Helper()
	files, dirs, digest, err := readArchive(context.Background(), bytes.NewReader(archive))
	if err == nil || files != nil || dirs != nil || digest != "" || len(err.Error()) > 300 {
		t.Fatalf("archive refusal: files=%d dirs=%d digest=%q err=%v", len(files), len(dirs), digest, err)
	}
}

func TestPAXResolvedPathAndValidNeighbor(t *testing.T) {
	longName := strings.Repeat("long-name-", 15) + ".md"
	entries := []entry{{"placeholder", tar.TypeReg, 4, []byte("long")}, {"neighbor.md", tar.TypeReg, 8, []byte("neighbor")}}
	archive := gzipTar(t, paxRaw(t, paxPathRecord(longName), entries...))
	files, _, digest, err := readArchive(context.Background(), bytes.NewReader(archive))
	if err != nil || !reflect.DeepEqual(files, map[string][]byte{longName: []byte("long"), "neighbor.md": []byte("neighbor")}) || digest != digestBytes(archive) {
		t.Fatalf("resolved name or neighbor lost: %v %v %q", err, files, digest)
	}
	for _, unsafe := range []string{"../escape", "/absolute", "a/../b", "a//b", "./name", "a\\b", "C:/name", "a/./b", "a\x00b", string([]byte{0xff}), ""} {
		t.Run(fmt.Sprintf("unsafe-%q", unsafe), func(t *testing.T) {
			// The physical file name and neighboring file remain valid; only
			// the resolved PAX path changes.
			archiveRefused(t, gzipTar(t, paxRaw(t, paxPathRecord(unsafe), entries...)))
		})
	}
}

func TestPAXDirectoryCollisionsAndSpecialTypes(t *testing.T) {
	name := strings.Repeat("directory-", 12) + "/"
	archive := gzipTar(t, paxRaw(t, paxPathRecord(name), entry{"placeholder/", tar.TypeDir, 0, nil}))
	files, dirs, _, err := readArchive(context.Background(), bytes.NewReader(archive))
	if err != nil || len(files) != 0 || !reflect.DeepEqual(dirs, []string{strings.TrimSuffix(name, "/")}) {
		t.Fatalf("resolved directory: %v %v", err, dirs)
	}
	for _, entries := range [][]entry{
		{{"placeholder", tar.TypeReg, 0, nil}, {"resolved", tar.TypeReg, 0, nil}},
		{{"placeholder/", tar.TypeDir, 0, nil}, {"resolved", tar.TypeReg, 0, nil}},
		{{"placeholder", tar.TypeSymlink, 0, nil}},
		{{"placeholder", tar.TypeLink, 0, nil}},
	} {
		archiveRefused(t, gzipTar(t, paxRaw(t, paxPathRecord("resolved"), entries...)))
	}
	archiveRefused(t, gzipTar(t, paxRaw(t, paxPathRecord("parent/child"), entry{"placeholder", tar.TypeReg, 0, nil}, entry{"parent", tar.TypeReg, 0, nil})))
}

func TestPAXMetadataFramingAndBounds(t *testing.T) {
	valid := paxPathRecord("resolved")
	for _, records := range [][]byte{
		nil, []byte("999 path=resolved\n"), bytes.TrimSuffix(valid, []byte("\n")),
		append(append([]byte{}, valid...), valid...), []byte("10 size=0\n"),
		append(append([]byte{}, valid...), []byte("10 size=0\n")...),
	} {
		archiveRefused(t, gzipTar(t, paxRaw(t, records, entry{"placeholder", tar.TypeReg, 0, nil})))
	}
	// A local path header must bind exactly the next ordinary entry.
	archiveRefused(t, gzipTar(t, paxRaw(t, valid)))
	raw := paxRaw(t, valid, entry{"placeholder", tar.TypeReg, 0, nil})
	prefix := append([]byte{}, raw[:1024]...)
	archiveRefused(t, gzipTar(t, append(prefix, raw...)))
	corrupt := append([]byte{}, raw...)
	corrupt[0] ^= 1
	archiveRefused(t, gzipTar(t, corrupt))
	corrupt = append([]byte{}, raw...)
	corrupt[512+len(valid)] = 1
	archiveRefused(t, gzipTar(t, corrupt))
	archiveRefused(t, gzipTar(t, raw[:520]))
	archiveRefused(t, gzipTar(t, paxHeader(t, MaxEntry+1)))
	// GNU and global extensions remain outside the accepted path-only subset.
	for _, typ := range []byte{tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink} {
		corrupt = append([]byte{}, raw...)
		corrupt[156] = typ
		archiveRefused(t, gzipTar(t, corrupt))
	}
	// Count physical metadata headers, including the valid boundary neighbor.
	files := map[string][]byte{}
	for i := 0; i < MaxEntries/2; i++ {
		files[strings.Repeat("x", 110)+fmt.Sprintf("-%05d", i)] = nil
	}
	if _, _, _, err := readArchive(context.Background(), bytes.NewReader(pack(t, files))); err != nil {
		t.Fatalf("exact physical-header budget refused: %v", err)
	}
	files[strings.Repeat("x", 110)+"-extra"] = nil
	archiveRefused(t, pack(t, files))
}

func TestLegacyLongPAXNamesPrepare(t *testing.T) {
	files := unpack(t, fixture(t, "legacy-v2"))
	const oldRoot = "EPIC-001_synthetic-epic"
	// A component longer than USTAR's prefix field forces a PAX path record.
	name := strings.Repeat("long-", 40) + "epic"
	for path, data := range files {
		if strings.HasPrefix(path, oldRoot+"/") {
			delete(files, path)
			files["EPIC-001_"+name+strings.TrimPrefix(path, oldRoot)] = data
		}
	}
	p := mustPrepare(t, pack(t, files))
	if len(p.Snapshot.Elements) != 7 || p.Snapshot.Elements[0].ID != "EPIC-001" || p.Snapshot.Elements[0].Name != name {
		t.Fatalf("long-name import changed facts: %+v", p.Snapshot.Elements)
	}
}

func TestNativeLongPAXNameChecksums(t *testing.T) {
	name := "provenance/" + strings.Repeat("p", 120) + ".txt"
	data := []byte("inert synthetic provenance")
	archive := func(digest string) []byte {
		return native(t, nil, func(m *nativeManifest, files map[string][]byte) {
			files[name] = data
			m.Files = append(m.Files, fileDigest{name, digest})
			sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
		})
	}
	p := mustPrepare(t, archive(digestBytes(data)))
	found := false
	for _, file := range p.Snapshot.Provenance {
		if file.Path == name {
			found = true
			if !bytes.Equal(file.Data, data) || file.SHA256 != digestBytes(data) {
				t.Fatal("resolved provenance changed")
			}
		}
	}
	if !found {
		t.Fatal("resolved provenance missing")
	}
	refused(t, archive(strings.Repeat("0", 64)))
}
