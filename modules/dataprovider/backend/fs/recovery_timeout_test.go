//go:build linux

package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

// This is a real common-duration startup timeout with retained on-disk intent,
// not a shortened timer, fake snapshot, or power-loss qualification.
func TestActualRecoveryTimeoutPreservesEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), dp.RecoveryTimeout+time.Minute)
	defer cancel()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	b := dp.Board{Scope: dp.Scope{ProductID: "alpha", BoardID: "project"}, Name: "Example"}
	if err := p.EnsureEmpty(ctx, b); err != nil {
		t.Fatal(err)
	}
	p.hook = func(_ context.Context, id string) error {
		if id == "cleanup" {
			return errors.New("synthetic retained cleanup intent")
		}
		return nil
	}
	a := dp.Actor{AccountID: "example", RequestID: "recovery-fixture", Operation: "put_resource", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	input := generated('r', 3)
	commit, err := p.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{PutResources: []dp.ResourceInput{input}}, nil
	})
	if code, _ := dp.CodeOf(err); code != dp.CommitOutcomeUnknown || commit.Revision != (dp.Revision{}) {
		t.Fatal("fixture did not retain unknown outcome", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprintTree(root)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	reached := false
	recovered, err := open(ctx, root, func(admission context.Context, id string) error {
		if id == "recovery" {
			reached = true
			<-admission.Done()
			return admission.Err()
		}
		return nil
	})
	elapsed := time.Since(started)
	if recovered != nil {
		_ = recovered.Close()
		t.Fatal("expired startup exposed provider")
	}
	if !reached || !errors.Is(err, context.DeadlineExceeded) || elapsed < dp.RecoveryTimeout || elapsed > dp.RecoveryTimeout+10*time.Second {
		t.Fatalf("actual recovery timeout reached=%v elapsed=%s err=%v", reached, elapsed, err)
	}
	t.Logf("actual startup/recovery elapsed=%s common_cap=%s error=%v", elapsed, dp.RecoveryTimeout, err)
	after, err := fingerprintTree(root)
	if err != nil || after != before {
		t.Fatal("expired recovery modified evidence", err)
	}
	for n := 0; n < 2; n++ {
		next, err := Open(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		cp, err := next.Checkpoint(ctx, b.Scope)
		if err != nil {
			_ = next.Close()
			t.Fatal(err)
		}
		snapshot := cp.Snapshot()
		if snapshot.Revision.Counter != 1 || len(snapshot.Resources) != 1 {
			_ = cp.Close()
			_ = next.Close()
			t.Fatal("recovery lost committed state")
		}
		if _, err := dp.EncodeCheckpoint(ctx, io.Discard, cp); err != nil {
			_ = next.Close()
			t.Fatal(err)
		}
		if err := next.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
