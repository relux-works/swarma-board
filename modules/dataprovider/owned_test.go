package dataprovider

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const abcSHA = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

func ownedFixture() (Snapshot, []Event) {
	scope := Scope{ProductID: "alpha", BoardID: "work"}
	revision := Revision{Scope: scope, Counter: 1}
	return Snapshot{Board: Board{Scope: scope}, Revision: revision, Elements: []Element{}, Provenance: []ProvenanceFile{}, Resources: []ResourceInfo{{SHA256: abcSHA, Size: 3}}}, []Event{{FormatVersion: 1, Scope: scope, Revision: revision, Actor: Actor{AccountID: "synthetic-editor", RequestID: "put", Operation: "put_resource", At: time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)}, ChangedIDs: []string{}, ChangedResourceIDs: []string{abcSHA}}}
}
func TestOwnedReaderVerifiesBeforeExposureAndReleasesExactlyOnce(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "corrupt"}[corrupt], func(t *testing.T) {
			var opens, releases atomic.Int32
			backing := ResourceBacking{Open: func(string) (io.ReadCloser, error) {
				opens.Add(1)
				raw := "abc"
				if corrupt {
					raw = "bad"
				}
				return io.NopCloser(strings.NewReader(raw)), nil
			}, Release: func() error { releases.Add(1); return nil }}
			r, err := OpenResourceReader(context.Background(), ResourceInfo{SHA256: abcSHA, Size: 3}, backing)
			if corrupt {
				requireCode(t, err, StorageError)
				if r.Reader != nil || opens.Load() != 1 || releases.Load() != 1 {
					t.Fatal("corrupt bytes exposed or backing leaked")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if opens.Load() != 2 || releases.Load() != 0 {
				t.Fatal("verification must finish before acquisition")
			}
			got, err := io.ReadAll(r.Reader)
			if err != nil || string(got) != "abc" {
				t.Fatal(string(got), err)
			}
			if err := r.Reader.Close(); err != nil {
				t.Fatal(err)
			}
			if err := r.Reader.Close(); err != nil {
				t.Fatal(err)
			}
			if releases.Load() != 1 {
				t.Fatal("backing not released exactly once")
			}
		})
	}
}
func TestOwnedCheckpointPinsDetachedMetadataAndOwnsSingleChild(t *testing.T) {
	snapshot, events := ownedFixture()
	var releases atomic.Int32
	cp, err := OpenCheckpoint(context.Background(), snapshot, events, ResourceBacking{Open: func(d string) (io.ReadCloser, error) {
		if d != abcSHA {
			t.Fatal("foreign lookup")
		}
		return io.NopCloser(strings.NewReader("abc")), nil
	}, Release: func() error { releases.Add(1); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	snapshot.Resources[0].Size = 999
	events[0].ChangedResourceIDs[0] = "alias"
	copy := cp.Snapshot()
	copy.Resources[0].Size = 888
	history := cp.Events()
	history[0].ChangedResourceIDs[0] = "alias"
	if cp.Snapshot().Resources[0].Size != 3 || cp.Events()[0].ChangedResourceIDs[0] != abcSHA {
		t.Fatal("metadata alias")
	}
	child, err := cp.OpenResource(abcSHA)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cp.OpenResource(abcSHA)
	requireCode(t, err, InvalidRequest)
	if second != nil {
		t.Fatal("second active reader")
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = cp.OpenResource(strings.Repeat("0", 64))
	requireCode(t, err, NotFound)
	child, err = cp.OpenResource(abcSHA)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(child)
	if err != nil || string(got) != "abc" {
		t.Fatal(string(got), err)
	}
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	if releases.Load() != 1 {
		t.Fatal("checkpoint backing released more than once")
	}
	_, err = child.Read(make([]byte, 1))
	requireCode(t, err, Unavailable)
	_, err = cp.OpenResource(abcSHA)
	requireCode(t, err, Unavailable)
	if cp.Snapshot().Resources[0].Size != 3 {
		t.Fatal("closed metadata lost")
	}
}
func TestOwnedContextCancellationReleasesUnclosedHandles(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "reader", true: "checkpoint"}[checkpoint], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			released := make(chan struct{})
			backing := ResourceBacking{Open: func(string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("abc")), nil }, Release: func() error { close(released); return nil }}
			var child io.ReadCloser
			var cp Checkpoint
			if checkpoint {
				snapshot, events := ownedFixture()
				var err error
				cp, err = OpenCheckpoint(ctx, snapshot, events, backing)
				if err != nil {
					t.Fatal(err)
				}
				child, err = cp.OpenResource(abcSHA)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				r, err := OpenResourceReader(ctx, ResourceInfo{SHA256: abcSHA, Size: 3}, backing)
				if err != nil {
					t.Fatal(err)
				}
				child = r.Reader
			}
			cancel()
			select {
			case <-released:
			case <-time.After(time.Second):
				t.Fatal("cancellation failed to release owned backing")
			}
			_, err := child.Read(make([]byte, 1))
			if !errors.Is(err, context.Canceled) {
				t.Fatal("context no longer recognizable", err)
			}
			if err := child.Close(); err != nil {
				t.Fatal(err)
			}
			if cp != nil {
				if err := cp.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestOwnedCheckpointLateFailureInvalidatesExportAndCloseIsStable(t *testing.T) {
	snapshot, events := ownedFixture()
	opens := 0
	releases := 0
	cp, err := OpenCheckpoint(context.Background(), snapshot, events, ResourceBacking{Open: func(string) (io.ReadCloser, error) {
		opens++
		if opens == 1 {
			return io.NopCloser(strings.NewReader("abc")), nil
		}
		return io.NopCloser(io.MultiReader(strings.NewReader("a"), failingReader{})), nil
	}, Release: func() error { releases++; return errors.New("private release cause") }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = EncodeCheckpoint(context.Background(), io.Discard, cp)
	requireCode(t, err, StorageError)
	first := cp.Close()
	second := cp.Close()
	requireCode(t, first, StorageError)
	if first != second || releases != 1 {
		t.Fatal("close error unstable or double release")
	}
	if strings.Contains(first.Error(), "private") {
		t.Fatal(first)
	}
}
