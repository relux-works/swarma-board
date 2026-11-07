package providerdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

type repeatedByte byte

func (b repeatedByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// This is an actual single 256MiB resource transaction, not a serialization
// argument about memory. Run alone under /usr/bin/time for process peak RSS;
// the JSON receipt also records Go allocation counters and actual elapsed time.
// It is not the platform/subtree/power-loss qualification of C32/C50.
func TestMaximumResourceTransaction(t *testing.T) {
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), dp.RecoveryTimeout)
	defer cancel()
	db, e := Open(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	board := dp.Board{Scope: dp.Scope{ProductID: "sample", BoardID: "resources"}, Name: "Resources"}
	if e = db.EnsureEmpty(ctx, board); e != nil {
		t.Fatal(e)
	}
	puts := []dp.ResourceInput{}
	for b := byte('a'); b <= 'h'; b++ {
		h := sha256.New()
		_, e := io.CopyN(h, repeatedByte(b), dp.MaxResourceBytes)
		if e != nil {
			t.Fatal(e)
		}
		info := dp.ResourceInfo{SHA256: hex.EncodeToString(h.Sum(nil)), Size: dp.MaxResourceBytes}
		puts = append(puts, dp.ResourceInput{Info: info, Reader: io.LimitReader(repeatedByte(b), info.Size)})
	}
	actor := dp.Actor{AccountID: "sample", RequestID: "maximum-resources", Operation: "put_resource", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now().UTC()
	commit, e := db.Update(ctx, board.Scope, dp.Revision{Scope: board.Scope}, actor, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{PutResources: puts}, nil })
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	var usage syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	receipt := struct {
		SubmittedBytes  int64     `json:"submitted_bytes"`
		Started         time.Time `json:"started"`
		ElapsedNS       int64     `json:"update_elapsed_ns"`
		HeapAllocBefore uint64    `json:"heap_alloc_before"`
		HeapAllocAfter  uint64    `json:"heap_alloc_after"`
		AllocatedDuring uint64    `json:"total_alloc_delta"`
		GoSysAfter      uint64    `json:"go_sys_after"`
		PeakRSSKiB      int64     `json:"process_peak_rss_kib_after_update"`
		DBBytes         int64     `json:"database_file_bytes"`
		Error           string    `json:"safe_error,omitempty"`
	}{SubmittedBytes: dp.MaxScopeResourceBytes, Started: started, ElapsedNS: int64(elapsed), HeapAllocBefore: before.HeapAlloc, HeapAllocAfter: after.HeapAlloc, AllocatedDuring: after.TotalAlloc - before.TotalAlloc, GoSysAfter: after.Sys, PeakRSSKiB: usage.Maxrss}
	if st, err := os.Stat(filepath.Join(root, "provider.db")); err == nil {
		receipt.DBBytes = st.Size()
	}
	if e != nil {
		receipt.Error = e.Error()
	}
	if path := os.Getenv("PROVIDERDB_MEMORY_REPORT"); path != "" {
		p, _ := json.MarshalIndent(receipt, "", "  ")
		if err := os.WriteFile(path, append(p, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if e != nil {
		t.Fatal(e)
	}
	if commit.Revision.Counter != 1 || len(commit.ChangedResourceIDs) != 8 {
		t.Fatal("incomplete resource transaction")
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = Open(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	cp, e := db.Checkpoint(ctx, board.Scope)
	if e != nil {
		t.Fatal(e)
	}
	var total int64
	for _, r := range cp.Snapshot().Resources {
		total += r.Size
	}
	if total != dp.MaxScopeResourceBytes {
		t.Fatal("wrong persisted total")
	}
	if _, e = dp.EncodeCheckpoint(ctx, io.Discard, cp); e != nil {
		t.Fatal(e)
	}
	t.Logf("single durable transaction submitted=%d update_elapsed=%s process_peak_rss_kib_after_update=%d", dp.MaxScopeResourceBytes, elapsed, usage.Maxrss)
}
