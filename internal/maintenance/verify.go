package maintenance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sort"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/store"
	"github.com/relux-works/swarma-board/internal/transfer"
)

// Integrity means internal structural/hash consistency only. An attacker able to
// replace all artifacts can recompute hashes. It conveys no signer authority.
type Integrity struct{ Manifest Manifest }

// Verify reads at most each artifact bound plus one overflow byte. Readers must
// obey context/deadline policy themselves if they can block on external I/O.
// Native structure/inner hashes use the frozen Transfer.Prepare validator; its
// returned import metadata is discarded and no import or storage write occurs.
func Verify(ctx context.Context, manifest, snapshot, journal io.Reader, limits Limits) (Integrity, error) {
	limits, e := limits.normalized()
	if e != nil {
		return Integrity{}, e
	}
	mb, e := readBounded(ctx, manifest, MaxManifest)
	if e != nil {
		return Integrity{}, e
	}
	var m Manifest
	if e = canonical(mb, &m); e != nil {
		return Integrity{}, e
	}
	if m.Format != Format || m.Version != 1 || m.Status != Unsigned || !store.ValidProjectID(m.ProjectID) || !label(m.ExporterCommit, false) || !label(m.DeclaredSignerKeyID, true) {
		return Integrity{}, ErrInvalid
	}
	first := uint64(0)
	if m.Revision > 0 {
		first = 1
	}
	if m.Journal != (Journal{Format: "swarma-board-audit-schema-v1", First: first, Last: m.Revision, Count: m.Revision}) {
		return Integrity{}, ErrInvalid
	}
	if m.Snapshot.Name != SnapshotName || m.Audit.Name != JournalName || m.Snapshot.Bytes < 1 || m.Snapshot.Bytes > limits.SnapshotBytes || m.Audit.Bytes < 0 || m.Audit.Bytes > limits.JournalBytes {
		return Integrity{}, ErrInvalid
	}
	sb, e := readBounded(ctx, snapshot, limits.SnapshotBytes)
	if e != nil {
		return Integrity{}, e
	}
	jb, e := readBounded(ctx, journal, limits.JournalBytes)
	if e != nil {
		return Integrity{}, e
	}
	if m.Snapshot != describe(SnapshotName, sb) || m.Audit != describe(JournalName, jb) {
		return Integrity{}, ErrInvalid
	}
	prepared, e := transfer.New(m.ExporterCommit).Prepare(ctx, bytes.NewReader(sb), contract.ImportOptions{ProjectID: m.ProjectID, ExpectedSHA256: m.Snapshot.SHA256})
	if e != nil {
		return Integrity{}, e
	}
	if uint64(prepared.Snapshot.Revision) != m.Revision || prepared.Report.Format != "swarma-board" || prepared.Report.Version != 1 {
		return Integrity{}, ErrInvalid
	}
	// Prepare has already checked every tar entry, byte bound, field and digest.
	// Read the validated inner manifest to bind its exporter commit as well.
	gz, e := gzip.NewReader(bytes.NewReader(sb))
	if e != nil {
		return Integrity{}, ErrInvalid
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return Integrity{}, ErrInvalid
		}
		if h.Name != ".swarma-board-transfer.json" {
			continue
		}
		var inner struct {
			SourceCommit string `json:"source_commit"`
		}
		if json.NewDecoder(tr).Decode(&inner) != nil || inner.SourceCommit != m.ExporterCommit {
			return Integrity{}, ErrInvalid
		}
		found = true
		break
	}
	if !found {
		return Integrity{}, ErrInvalid
	}
	var revision uint64
	for len(jb) > 0 {
		if e = ctx.Err(); e != nil {
			return Integrity{}, e
		}
		end := bytes.IndexByte(jb, '\n')
		if end < 0 {
			return Integrity{}, ErrInvalid
		}
		line := jb[:end+1]
		jb = jb[end+1:]
		var event store.AuditEvent
		if e = canonical(line, &event); e != nil {
			return Integrity{}, e
		}
		if revision >= m.Revision || uint64(event.Revision) != revision+1 || !sort.StringsAreSorted(event.ChangedIDs) {
			return Integrity{}, ErrInvalid
		}
		for i, id := range event.ChangedIDs {
			if id == "" || i > 0 && id == event.ChangedIDs[i-1] {
				return Integrity{}, ErrInvalid
			}
		}
		revision++
	}
	if revision != m.Revision {
		return Integrity{}, ErrInvalid
	}
	if e = ctx.Err(); e != nil {
		return Integrity{}, e
	}
	return Integrity{Manifest: m}, nil
}

func canonical(b []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return ErrInvalid
	}
	canonical, e := marshal(dst)
	if e != nil || !bytes.Equal(b, canonical) {
		return ErrInvalid
	}
	return nil
}
func readBounded(ctx context.Context, r io.Reader, max int64) ([]byte, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	var b bytes.Buffer
	_, e := io.Copy(&boundedWriter{ctx: ctx, dst: &b, max: max}, io.LimitReader(r, max+1))
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}

// ExternalSigner and ProtectedVerifier are injected custody-policy seams, not
// platform APIs. No implementation, algorithm, key lookup or default is provided.
// Both receive exact manifest bytes. The trusted caller selects the approved key
// identity independently of untrusted evidence; the verifier must enforce the
// owner-approved trust anchor, signature format/algorithm and custody policy.
type ExternalSigner interface {
	SignManifest(context.Context, string, []byte) ([]byte, error)
}
type ProtectedVerifier interface {
	VerifyManifest(context.Context, string, []byte, []byte) error
}

// RequestSignature returns opaque candidate bytes only, never signed success.
func RequestSignature(ctx context.Context, b Bundle, approvedKeyID string, signer ExternalSigner) ([]byte, error) {
	v, e := Verify(ctx, bytes.NewReader(b.Manifest), bytes.NewReader(b.Snapshot), bytes.NewReader(b.Journal), Limits{})
	if e != nil {
		return nil, e
	}
	if nilDependency(signer) || approvedKeyID == "" || approvedKeyID != v.Manifest.DeclaredSignerKeyID {
		return nil, ErrSignatureGate
	}
	sig, e := signer.SignManifest(ctx, approvedKeyID, bytes.Clone(b.Manifest))
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	if len(sig) == 0 || len(sig) > MaxManifest {
		return nil, ErrSignatureGate
	}
	return bytes.Clone(sig), nil
}

// VerifyProtected is a separate acceptance gate. Success means only that the
// injected protected policy accepted these exact bytes. No implementation or
// production custody approval ships here; the manifest stays labeled unsigned.
func VerifyProtected(ctx context.Context, b Bundle, signature []byte, approvedKeyID string, verifier ProtectedVerifier) error {
	v, e := Verify(ctx, bytes.NewReader(b.Manifest), bytes.NewReader(b.Snapshot), bytes.NewReader(b.Journal), Limits{})
	if e != nil {
		return e
	}
	if nilDependency(verifier) || approvedKeyID == "" || approvedKeyID != v.Manifest.DeclaredSignerKeyID || len(signature) == 0 || len(signature) > MaxManifest {
		return ErrSignatureGate
	}
	if e = verifier.VerifyManifest(ctx, approvedKeyID, bytes.Clone(b.Manifest), bytes.Clone(signature)); e != nil {
		return e
	}
	return ctx.Err()
}

// Interface values containing nil implementations also refuse before invocation.
func nilDependency(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}
