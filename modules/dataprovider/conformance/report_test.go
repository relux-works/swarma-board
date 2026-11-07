package conformance

import (
	"context"
	"errors"
	dp "github.com/relux-works/swarma-board/dataprovider"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

// Synthetic report shape only; no fabricated row is evidence of backend
// behavior, shared integration or native/workload qualification.
func structuralReport(t *testing.T) Report {
	t.Helper()
	v, err := Vectors()
	must(t, err)
	r := Report{Provider: "filesystem", SpecSHA256: SpecSHA256, VectorsSHA256: VectorsSHA256}
	for _, c := range v.Cases {
		row := Result{ID: c.ID, Layer: c.Layer, Status: Verified}
		if c.Layer == "provider" {
			for _, s := range v.Fixture.Scopes {
				scope := dp.Scope{ProductID: s.ProductID, BoardID: s.BoardID}
				row.Observations = append(row.Observations, CanonicalObservation{Scope: scope, Revision: dp.Revision{Scope: scope, Counter: 1}, SHA256: strings.Repeat("a", 64), Bytes: 246})
			}
			sort.Slice(row.Observations, func(i, j int) bool {
				a, b := row.Observations[i], row.Observations[j]
				if a.Scope.ProductID != b.Scope.ProductID {
					return a.Scope.ProductID < b.Scope.ProductID
				}
				return a.Scope.BoardID < b.Scope.BoardID
			})
		}
		r.Results = append(r.Results, row)
	}
	return r
}
func malformedReport(t *testing.T, mode string) Report {
	r := structuralReport(t)
	switch mode {
	case "duplicate":
		for i := range r.Results {
			r.Results[i] = r.Results[0]
		}
	case "missing":
		r.Results = r.Results[:49]
	case "unknown":
		r.Results[49].ID = "C51"
	case "reordered":
		r.Results[0], r.Results[1] = r.Results[1], r.Results[0]
	case "layer":
		r.Results[0].Layer = "server"
	case "spec":
		r.SpecSHA256 = strings.Repeat("b", 64)
	case "vectors":
		r.VectorsSHA256 = strings.Repeat("b", 64)
	case "provider":
		r.Provider = "synthetic"
	case "status":
		r.Results[0].Status = NotVerified
	case "partial-detail":
		r.Results[0].Detail = "incomplete"
	case "no-observations":
		r.Results[17].Observations = nil
	case "missing-scope":
		r.Results[0].Observations = r.Results[0].Observations[:1]
	case "bad-scope":
		r.Results[0].Observations[0].Scope.ProductID = "../x"
	case "foreign-revision":
		r.Results[0].Observations[0].Revision.Scope.ProductID = "foreign"
	case "digest":
		r.Results[0].Observations[0].SHA256 = "not-a-digest"
	case "zero-bytes":
		r.Results[0].Observations[0].Bytes = 0
	case "oversize":
		r.Results[0].Observations[0].Bytes = dp.MaxCheckpointBytes + 1
	case "duplicate-observation":
		r.Results[0].Observations = append(r.Results[0].Observations, r.Results[0].Observations[0])
	case "observation-order":
		r.Results[0].Observations[0], r.Results[0].Observations[1] = r.Results[0].Observations[1], r.Results[0].Observations[0]
	default:
		t.Fatal("unknown malformed report control")
	}
	return r
}
func TestReportGatesRejectMalformedFrozenShape(t *testing.T) {
	modes := []string{"duplicate", "missing", "unknown", "reordered", "layer", "spec", "vectors", "provider", "status", "partial-detail", "no-observations", "missing-scope", "bad-scope", "foreign-revision", "digest", "zero-bytes", "oversize", "duplicate-observation", "observation-order"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			if malformedReport(t, mode).AllVerified() {
				t.Fatal("AllVerified accepted malformed report")
			}
			for _, gate := range []string{"require", "compare"} {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				exe, err := os.Executable()
				must(t, err)
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestMalformedReportGateChild$", "-test.v")
				cmd.Env = append(os.Environ(), "CONFORMANCE_REPORT_MODE="+mode, "CONFORMANCE_REPORT_GATE="+gate)
				out, err := cmd.CombinedOutput()
				cancel()
				var status *exec.ExitError
				if !errors.As(err, &status) || status.ExitCode() != 1 {
					t.Fatalf("%s accepted %s: exit=%v output=%s", gate, mode, err, out)
				}
				t.Logf("%s rejects %s: child exit=1", gate, mode)
			}
		})
	}
}
func TestMalformedReportGateChild(t *testing.T) {
	mode := os.Getenv("CONFORMANCE_REPORT_MODE")
	if mode == "" {
		return
	}
	r := malformedReport(t, mode)
	if os.Getenv("CONFORMANCE_REPORT_GATE") == "require" {
		r.RequireAll(t)
		return
	}
	db := structuralReport(t)
	db.Provider = "database"
	Compare(t, r, db)
}
func TestStructuralReportGatesAndEveryRequiredProviderObservation(t *testing.T) {
	r := structuralReport(t)
	if !r.AllVerified() {
		t.Fatal("complete structural fixture refused")
	}
	r.RequireAll(t)
	db := structuralReport(t)
	db.Provider = "database"
	Compare(t, r, db)
	for i, c := range r.Results {
		if c.Layer == "provider" {
			bad := structuralReport(t)
			bad.Results[i].Observations = nil
			if bad.AllVerified() {
				t.Fatalf("%s verified without required observations", c.ID)
			}
		}
	}
}
