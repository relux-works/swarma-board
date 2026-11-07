package providerdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
)

type custodyRecord struct {
	File       string    `json:"file"`
	Label      string    `json:"label"`
	SHA256     string    `json:"sha256"`
	Bytes      int64     `json:"bytes"`
	CapturedAt time.Time `json:"captured_at"`
}

// The destination belongs to the parent; each worker fixture belongs to a child.
// Assertions after t.Run returns observe the actual Go fixture cleanup boundary.
func TestFaultEvidenceCustody(t *testing.T) {
	parent := t.TempDir()
	if external := os.Getenv("CUSTODY_TEST_EVIDENCE"); external != "" {
		if !filepath.IsAbs(external) {
			t.Fatal("custody test evidence must be absolute")
		}
		if err := os.MkdirAll(external, 0700); err != nil {
			t.Fatal(err)
		}
		var err error
		parent, err = os.MkdirTemp(external, "custody-")
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"configured", "unconfigured", "pause-unconfigured", "inside-fixture", "fixture-itself", "manifest-unavailable", "symlink-destination"} {
		destination := filepath.Join(parent, name)
		var root string
		var expected []byte
		mode := cf.DefiniteFailure
		if name == "pause-unconfigured" {
			mode = cf.Pause
		}
		requiredFailure := name != "configured" && name != "pause-unconfigured"
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			root = t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "unconfigured", "pause-unconfigured":
				destination = ""
			case "inside-fixture":
				destination = filepath.Join(root, "evidence")
			case "fixture-itself":
				destination = root
			case "manifest-unavailable":
				if err := os.MkdirAll(filepath.Join(destination, "manifest.jsonl"), 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink-destination":
				if err := os.Symlink(root, destination); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PROVIDERDB_EVIDENCE", destination)
			driver := newDriver()
			instance, err := factory(driver).Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer instance.Cleanup()
			board := dp.Board{Scope: dp.Scope{ProductID: "sample", BoardID: "custody"}, Name: "Custody"}
			if err = instance.Provisioner.EnsureEmpty(ctx, board); err != nil {
				t.Fatal(err)
			}
			var point cf.FaultPoint
			for _, p := range driver.Points() {
				if p.ID == "before-commit" {
					point = p
				}
			}
			ticket, err := driver.Arm(ctx, root, point, mode)
			if err != nil {
				t.Fatal(err)
			}
			defer ticket.Close()
			done := make(chan error, 1)
			go func() {
				_, e := instance.Provider.Update(ctx, board.Scope, dp.Revision{Scope: board.Scope}, dp.Actor{AccountID: "sample", RequestID: "custody", Operation: "set_notes", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil })
				done <- e
			}()
			if err = ticket.WaitReached(ctx); err != nil {
				t.Fatal(err)
			}
			expected, err = os.ReadFile(filepath.Join(root, "provider.db"))
			if err != nil {
				t.Fatal(err)
			}
			receipt, triggerErr := ticket.Trigger(ctx)
			if requiredFailure {
				if triggerErr == nil || receipt.EvidenceRetained {
					t.Fatalf("unavailable custody must fail without a retained receipt: receipt=%+v err=%v", receipt, triggerErr)
				}
			} else {
				if triggerErr != nil {
					t.Fatal(triggerErr)
				}
				if receipt.EvidenceRetained != (name == "configured") {
					t.Fatalf("untruthful retention: %+v", receipt)
				}
				if receipt.PointID != point.ID || receipt.Mode != mode || receipt.ReachedAt.IsZero() || receipt.TriggeredAt.Before(receipt.ReachedAt) {
					t.Fatalf("unbound receipt: %+v", receipt)
				}
			}
			// A failed capture must leave Close able to release the parked worker.
			if err = ticket.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("worker remained parked after custody refusal")
			}
			if name == "configured" {
				if code, ok := dp.CodeOf(err); !ok || code != dp.StorageError {
					t.Fatalf("fault was not injected: %v", err)
				}
			} else if err != nil {
				t.Fatalf("released worker failed: %v", err)
			}
			t.Logf("required_failure=%t trigger_error=%v evidence_retained=%t", requiredFailure, triggerErr, receipt.EvidenceRetained)
		})
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("fixture cleanup did not remove source: %v", err)
		}
		if name != "configured" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(destination, "manifest.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		var record custodyRecord
		if err = json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(record.File) != record.File || record.CapturedAt.IsZero() || record.Label != "before-commit-"+string(mode) {
			t.Fatalf("invalid manifest: %+v", record)
		}
		physical, err := os.ReadFile(filepath.Join(destination, record.File))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(physical)
		if !bytes.Equal(expected, physical) || record.Bytes != int64(len(physical)) || record.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("post-cleanup physical bytes/hash/manifest differ: %+v", record)
		}
		t.Logf("post-cleanup physical custody: file=%s bytes=%d sha256=%s", record.File, record.Bytes, record.SHA256)
	}
}

func TestCaptureCustodyBounds(t *testing.T) {
	for _, name := range []string{"relative-destination", "source-symlink", "oversize", "aggregate-cap", "destination-mode", "manifest-symlink", "manifest-pipe"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			destination := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(destination, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "provider.db")
			if err := os.WriteFile(path, []byte("bounded synthetic custody control"), 0600); err != nil {
				t.Fatal(err)
			}
			driver := newDriver()
			switch name {
			case "relative-destination":
				destination = "relative-evidence"
			case "source-symlink":
				if err := os.Rename(path, filepath.Join(root, "original.db")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "original.db"), path); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.Truncate(path, (2<<20)+1); err != nil {
					t.Fatal(err)
				}
			case "aggregate-cap":
				driver.evidenceBytes = 16 << 20
			case "destination-mode":
				if err := os.Chmod(destination, 0755); err != nil {
					t.Fatal(err)
				}
			case "manifest-pipe":
				if err := syscall.Mkfifo(filepath.Join(destination, "manifest.jsonl"), 0600); err != nil {
					t.Fatal(err)
				}
			case "manifest-symlink":
				if err := os.Symlink(filepath.Join(root, "original-manifest"), filepath.Join(destination, "manifest.jsonl")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PROVIDERDB_EVIDENCE", destination)
			retained, err := driver.capture(root, "bounded-control")
			if err == nil || retained {
				t.Fatalf("unsafe or over-budget custody accepted: retained=%t err=%v", retained, err)
			}
		})
	}
}
