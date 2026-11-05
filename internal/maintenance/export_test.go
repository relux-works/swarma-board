package maintenance

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/store"
	"github.com/relux-works/swarma-board/internal/transfer"
	bolt "go.etcd.io/bbolt"
)

var testProject = contract.Project{ID: "synthetic-board", Name: "Synthetic only"}
var testContext = context.Background()

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func actor(n string) contract.AuditContext {
	return contract.AuditContext{AccountID: "synthetic-editor", RequestID: n, Operation: "synthetic-maintenance-test", At: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
}
func seed(t *testing.T) (Options, contract.Snapshot) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.db")
	db, e := store.Open(path, testProject, true)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../testadapter/testdata/native-v1.tar.gz")
	if e != nil {
		t.Fatal(e)
	}
	p, e := transfer.New(testCommit).Prepare(testContext, bytes.NewReader(b), contract.ImportOptions{ProjectID: testProject.ID})
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Update(testContext, testProject.ID, 0, actor("seed"), func(contract.Snapshot) (contract.ChangeSet, error) {
		return contract.ChangeSet{Upserts: p.Snapshot.Elements, Provenance: p.Snapshot.Provenance}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Update(testContext, testProject.ID, 1, actor("post-checkpoint"), func(s contract.Snapshot) (contract.ChangeSet, error) {
		var changes []contract.Element
		for _, x := range s.Elements {
			if x.Type == "task" {
				x.Notes += "\nsynthetic post-cutover evidence"
				changes = append(changes, x)
				break
			}
		}
		if len(changes) == 0 {
			t.Fatal("fixture has no task")
		}
		return contract.ChangeSet{Upserts: changes}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	s, events, e := db.ExportView(testContext, testProject.ID)
	if e != nil || len(events) != 2 {
		t.Fatal(e, len(events))
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	return Options{Path: path, Project: testProject, WriterStopped: true, ExporterCommit: testCommit, DeclaredSignerKeyID: "unapproved-synthetic-key"}, s
}
func verify(b Bundle, l Limits) (Integrity, error) {
	return Verify(testContext, bytes.NewReader(b.Manifest), bytes.NewReader(b.Snapshot), bytes.NewReader(b.Journal), l)
}
func exported(t *testing.T) (Options, Bundle) {
	t.Helper()
	o, _ := seed(t)
	b, e := Export(testContext, o)
	if e != nil {
		t.Fatal(e)
	}
	return o, b
}
func requireZero(t *testing.T, b Bundle, e error) {
	t.Helper()
	if e == nil || !reflect.DeepEqual(b, Bundle{}) {
		t.Fatalf("want refusal and zero bundle; err=%v", e)
	}
}
func clone(b Bundle) Bundle {
	return Bundle{bytes.Clone(b.Manifest), bytes.Clone(b.Snapshot), bytes.Clone(b.Journal)}
}
func editManifest(t *testing.T, b *Bundle, edit func(*Manifest)) {
	t.Helper()
	var m Manifest
	if e := json.Unmarshal(b.Manifest, &m); e != nil {
		t.Fatal(e)
	}
	edit(&m)
	var e error
	b.Manifest, e = marshal(m)
	if e != nil {
		t.Fatal(e)
	}
}
func rehash(t *testing.T, b *Bundle) {
	editManifest(t, b, func(m *Manifest) {
		m.Snapshot = describe(SnapshotName, b.Snapshot)
		m.Audit = describe(JournalName, b.Journal)
	})
}
func dbBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestExportExactNativeAndNoWrites(t *testing.T) {
	o, s := seed(t)
	before := dbBytes(t, o.Path)
	st, e := os.Stat(o.Path)
	if e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(filepath.Dir(o.Path))
	if e != nil {
		t.Fatal(e)
	}
	b, e := Export(testContext, o)
	if e != nil {
		t.Fatal(e)
	}
	v, e := verify(b, Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if v.Manifest.Status != Unsigned || v.Manifest.Revision != 2 || v.Manifest.Journal.Count != 2 {
		t.Fatal(v)
	}
	var native bytes.Buffer
	if e = transfer.New(testCommit).Encode(testContext, &native, s); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(native.Bytes(), b.Snapshot) {
		t.Fatal("changed native snapshot encoding")
	}
	if !bytes.Contains(b.Journal, []byte("post-checkpoint")) {
		t.Fatal("lost post-checkpoint event")
	}
	if bytes.Count(b.Journal, []byte{'\n'}) != 2 {
		t.Fatal("journal not complete")
	}
	again, e := Export(testContext, o)
	if e != nil || !reflect.DeepEqual(b, again) {
		t.Fatal("nondeterministic checkpoint", e)
	}
	afterSt, e := os.Stat(o.Path)
	if e != nil {
		t.Fatal(e)
	}
	afterEntries, e := os.ReadDir(filepath.Dir(o.Path))
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, dbBytes(t, o.Path)) || st.Mode() != afterSt.Mode() || !st.ModTime().Equal(afterSt.ModTime()) || len(entries) != len(afterEntries) {
		t.Fatal("export wrote source or sidecar")
	}
	// Evidence generation is explicit and test-only, always using the temp DB.
	// No real source path can enter this harness.
	if dir := os.Getenv("MAINTENANCE_SYNTHETIC_EVIDENCE_DIR"); dir != "" {
		if e = os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
		for name, data := range map[string][]byte{ManifestName: b.Manifest, SnapshotName: b.Snapshot, JournalName: b.Journal} {
			f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				t.Fatal(e)
			}
			_, we := f.Write(data)
			ce := f.Close()
			if we != nil || ce != nil {
				t.Fatal(we, ce)
			}
		}
	}
}

func TestRequiresStoppedWriterAndBusyRefuses(t *testing.T) {
	o, _ := seed(t)
	o.WriterStopped = false
	b, e := Export(testContext, o)
	requireZero(t, b, e)
	if !errors.Is(e, ErrWriterNotStopped) {
		t.Fatal(e)
	}
	o.WriterStopped = true
	db, e := store.Open(o.Path, o.Project, false)
	if e != nil {
		t.Fatal(e)
	}
	before := dbBytes(t, o.Path)
	start := time.Now()
	b, e = Export(testContext, o)
	elapsed := time.Since(start)
	requireZero(t, b, e)
	if !errors.Is(e, bolt.ErrTimeout) || elapsed > 4*time.Second {
		t.Fatal("busy did not fail with bounded timeout", e, elapsed)
	}
	if !bytes.Equal(before, dbBytes(t, o.Path)) {
		t.Fatal("busy export wrote DB")
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	// The read-only handle excludes writers, and cannot execute a mutation callback.
	ro, e := store.OpenReadOnly(o.Path, o.Project)
	if e != nil {
		t.Fatal(e)
	}
	called := false
	_, e = ro.Update(testContext, o.Project.ID, 2, actor("refused"), func(contract.Snapshot) (contract.ChangeSet, error) { called = true; return contract.ChangeSet{}, nil })
	if !errors.Is(e, bolt.ErrDatabaseReadOnly) || called {
		t.Fatal("reader wrote", e, called)
	}
	writer, e := bolt.Open(o.Path, 0600, &bolt.Options{Timeout: 50 * time.Millisecond})
	if e == nil {
		writer.Close()
		t.Fatal("writer entered read-only lock")
	}
	if !errors.Is(e, bolt.ErrTimeout) {
		t.Fatal(e)
	}
	if e = ro.Close(); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, dbBytes(t, o.Path)) {
		t.Fatal("read-only refusal wrote DB")
	}
}

func TestNoCreateNoInitializationAndWrongProject(t *testing.T) {
	dir := t.TempDir()
	o := Options{Project: testProject, WriterStopped: true, ExporterCommit: testCommit}
	for _, name := range []string{"missing", "empty", "legacy", "unversioned", "future-schema", "symlink", "public"} {
		t.Run(name, func(t *testing.T) {
			o.Path = filepath.Join(dir, name)
			switch name {
			case "empty":
				if e := os.WriteFile(o.Path, nil, 0600); e != nil {
					t.Fatal(e)
				}
			case "legacy":
				if e := os.WriteFile(o.Path, []byte("synthetic legacy"), 0600); e != nil {
					t.Fatal(e)
				}
			case "unversioned", "future-schema":
				db, e := bolt.Open(o.Path, 0600, nil)
				if e != nil {
					t.Fatal(e)
				}
				if name == "future-schema" {
					e = db.Update(func(tx *bolt.Tx) error {
						b, e := tx.CreateBucket([]byte("board-v1"))
						if e != nil {
							return e
						}
						var v [8]byte
						binary.BigEndian.PutUint64(v[:], 2)
						return b.Put([]byte("schema"), v[:])
					})
					if e != nil {
						t.Fatal(e)
					}
				}
				if e = db.Close(); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Symlink(filepath.Join(dir, "unversioned"), o.Path); e != nil {
					t.Fatal(e)
				}
			case "public":
				if e := os.WriteFile(o.Path, []byte("synthetic"), 0644); e != nil {
					t.Fatal(e)
				}
			}
			before, _ := os.ReadFile(o.Path)
			b, e := Export(testContext, o)
			requireZero(t, b, e)
			if name == "missing" {
				if _, e = os.Lstat(o.Path); !os.IsNotExist(e) {
					t.Fatal("created missing DB", e)
				}
			} else if !bytes.Equal(before, dbBytes(t, o.Path)) {
				t.Fatal("initialized or changed refused database")
			}
		})
	}
	o, _ = seed(t)
	before := dbBytes(t, o.Path)
	o.Project.ID = "other"
	b, e := Export(testContext, o)
	requireZero(t, b, e)
	if !bytes.Equal(before, dbBytes(t, o.Path)) {
		t.Fatal("wrong-project refusal wrote DB")
	}
}

func TestEmptyRevisionAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-project.db")
	db, e := store.Open(path, testProject, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	o := Options{Path: path, Project: testProject, WriterStopped: true, ExporterCommit: testCommit}
	b, e := Export(testContext, o)
	if e != nil {
		t.Fatal(e)
	}
	v, e := verify(b, Limits{})
	if e != nil || v.Manifest.Revision != 0 || v.Manifest.Journal.First != 0 || len(b.Journal) != 0 {
		t.Fatal(v, e)
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	out, e := Export(ctx, o)
	requireZero(t, out, e)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = Verify(ctx, bytes.NewReader(b.Manifest), bytes.NewReader(b.Snapshot), bytes.NewReader(b.Journal), Limits{}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestArtifactBoundsAndNoPartialOutput(t *testing.T) {
	o, b := exported(t)
	before := dbBytes(t, o.Path)
	exact := Limits{SnapshotBytes: int64(len(b.Snapshot)), JournalBytes: int64(len(b.Journal))}
	if _, e := verify(b, exact); e != nil {
		t.Fatal("exact bound rejected", e)
	}
	o.Limits = exact
	if _, e := Export(testContext, o); e != nil {
		t.Fatal(e)
	}
	cases := []Limits{{SnapshotBytes: exact.SnapshotBytes - 1}, {JournalBytes: exact.JournalBytes - 1}, {SnapshotBytes: -1}, {JournalBytes: store.JournalLimit + 1}, {SnapshotBytes: transfer.MaxCompressed + 1}}
	for _, l := range cases {
		o.Limits = l
		out, e := Export(testContext, o)
		requireZero(t, out, e)
		if _, e = verify(b, l); e == nil {
			t.Fatal("verifier ignored bound", l)
		}
	}
	if !bytes.Equal(before, dbBytes(t, o.Path)) {
		t.Fatal("bounds refusal wrote DB")
	}
	if _, e := Verify(testContext, strings.NewReader(strings.Repeat("x", MaxManifest+1)), nil, nil, Limits{}); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
	for _, n := range []int{0, 1, 2, 31, 32, 33} {
		data := strings.Repeat("x", n)
		got, e := readBounded(testContext, strings.NewReader(data), 32)
		if n <= 32 && (e != nil || string(got) != data) || n > 32 && !errors.Is(e, ErrLimit) {
			t.Fatal(n, e)
		}
	}
	w := &boundedWriter{ctx: testContext, dst: shortWriter{}, max: 100}
	if _, e := w.Write([]byte("abc")); !errors.Is(e, io.ErrShortWrite) {
		t.Fatal(e)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestTamperAndStructuralRefusals(t *testing.T) {
	_, original := exported(t)
	cases := map[string]func(*Bundle){
		"archive-byte":       func(b *Bundle) { b.Snapshot[len(b.Snapshot)/2] ^= 1 },
		"journal-byte":       func(b *Bundle) { b.Journal[10] ^= 1 },
		"manifest-byte":      func(b *Bundle) { b.Manifest[10] ^= 1 },
		"manifest-unknown":   func(b *Bundle) { b.Manifest = append([]byte("{\"unknown\":1,"), b.Manifest[1:]...) },
		"manifest-duplicate": func(b *Bundle) { b.Manifest = append([]byte("{\"version\":1,"), b.Manifest[1:]...) },
		"signed-status":      func(b *Bundle) { editManifest(t, b, func(m *Manifest) { m.Status = "signed" }) },
		"wrong-project":      func(b *Bundle) { editManifest(t, b, func(m *Manifest) { m.ProjectID = "other" }) },
		"wrong-exporter":     func(b *Bundle) { editManifest(t, b, func(m *Manifest) { m.ExporterCommit = "other" }) },
		"wrong-range":        func(b *Bundle) { editManifest(t, b, func(m *Manifest) { m.Journal.First = 0 }) },
		"wrong-revision": func(b *Bundle) {
			editManifest(t, b, func(m *Manifest) { m.Revision = 3; m.Journal.Last = 3; m.Journal.Count = 3 })
		},
		"bad-name":               func(b *Bundle) { editManifest(t, b, func(m *Manifest) { m.Audit.Name = "../audit" }) },
		"rehash-corrupt-archive": func(b *Bundle) { b.Snapshot[len(b.Snapshot)/2] ^= 1; rehash(t, b) },
		"trailing-snapshot":      func(b *Bundle) { b.Snapshot = append(b.Snapshot, 'x'); rehash(t, b) },
		"missing-event":          func(b *Bundle) { b.Journal = b.Journal[bytes.IndexByte(b.Journal, '\n')+1:]; rehash(t, b) },
		"duplicate-event": func(b *Bundle) {
			b.Journal = append(b.Journal, b.Journal[:bytes.IndexByte(b.Journal, '\n')+1]...)
			rehash(t, b)
		},
		"reordered-events": func(b *Bundle) {
			i := bytes.IndexByte(b.Journal, '\n') + 1
			b.Journal = append(bytes.Clone(b.Journal[i:]), b.Journal[:i]...)
			rehash(t, b)
		},
		"missing-lf":           func(b *Bundle) { b.Journal = b.Journal[:len(b.Journal)-1]; rehash(t, b) },
		"unknown-event":        func(b *Bundle) { b.Journal = append([]byte("{\"Unknown\":1,"), b.Journal[1:]...); rehash(t, b) },
		"duplicate-field":      func(b *Bundle) { b.Journal = append([]byte("{\"Revision\":1,"), b.Journal[1:]...); rehash(t, b) },
		"noncanonical-journal": func(b *Bundle) { b.Journal = append([]byte(" "), b.Journal...); rehash(t, b) },
		"null-event":           func(b *Bundle) { b.Journal = []byte("null\n"); rehash(t, b) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := clone(original)
			change(&b)
			v, e := verify(b, Limits{})
			if e == nil || !reflect.DeepEqual(v, Integrity{}) {
				t.Fatal("tamper admitted", e)
			}
		})
	}
}

func TestCorruptSourceJournalRefusesReadOnly(t *testing.T) {
	for _, kind := range []string{"gap", "unknown", "over-limit"} {
		t.Run(kind, func(t *testing.T) {
			o, _ := seed(t)
			db, e := bolt.Open(o.Path, 0600, nil)
			if e != nil {
				t.Fatal(e)
			}
			e = db.Update(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte("audit"))
				var k [8]byte
				binary.BigEndian.PutUint64(k[:], 1)
				switch kind {
				case "gap":
					return b.Delete(k[:])
				case "unknown":
					return b.Put(k[:], []byte(`{"Revision":1,"Unknown":true}`))
				default:
					return b.Put(k[:], bytes.Repeat([]byte{'x'}, store.JournalLimit))
				}
			})
			if e != nil {
				t.Fatal(e)
			}
			if e = db.Close(); e != nil {
				t.Fatal(e)
			}
			before := dbBytes(t, o.Path)
			out, e := Export(testContext, o)
			requireZero(t, out, e)
			if !bytes.Equal(before, dbBytes(t, o.Path)) {
				t.Fatal("corrupt source changed")
			}
		})
	}
}

func TestNativeEntryOverflowRefusesWithoutWrites(t *testing.T) {
	o, _ := seed(t)
	db, e := store.Open(o.Path, o.Project, false)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Update(testContext, o.Project.ID, 2, actor("synthetic-oversize"), func(s contract.Snapshot) (contract.ChangeSet, error) {
		x := s.Elements[0]
		x.Notes = strings.Repeat("x", int(transfer.MaxEntry)+1)
		return contract.ChangeSet{Upserts: []contract.Element{x}}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	before := dbBytes(t, o.Path)
	out, e := Export(testContext, o)
	requireZero(t, out, e)
	if !bytes.Equal(before, dbBytes(t, o.Path)) {
		t.Fatal("native entry refusal changed source")
	}
}

type countingReader struct{ left, n int64 }

func (r *countingReader) Read(b []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(b)), r.left)
	clear(b[:n])
	r.left -= n
	r.n += n
	return int(n), nil
}
func TestBoundedReaderDoesNotConsumeUnboundedInput(t *testing.T) {
	r := &countingReader{left: 1 << 30}
	if _, e := readBounded(testContext, r, 32); !errors.Is(e, ErrLimit) || r.n != 33 {
		t.Fatal(e, r.n)
	}
	if _, e := readBounded(testContext, nil, 32); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
