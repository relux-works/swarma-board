package conformance

import "testing"

// Compare requires both real factories' complete results and compares canonical
// modeled outcomes exactly. A missing/partial result remains NOT_VERIFIED and
// fails this gate. No Actor, scope, revision, ordering, resource, or semantic
// error is removed. Fault scenarios additionally assert their permitted old/new
// outcomes directly in the runner; native qualification remains separate.
func Compare(t *testing.T, filesystem, database Report) {
	t.Helper()
	if filesystem.Provider != "filesystem" || database.Provider != "database" {
		t.Error("comparison requires both real adapter reports")
		return
	}
	if filesystem.SpecSHA256 != SpecSHA256 || database.SpecSHA256 != SpecSHA256 || filesystem.VectorsSHA256 != VectorsSHA256 || database.VectorsSHA256 != VectorsSHA256 {
		t.Error("comparison requires identical frozen spec/vectors")
		return
	}
	if len(filesystem.Results) != 50 || len(database.Results) != 50 {
		t.Error("comparison requires fifty results per factory")
		return
	}
	for i, a := range filesystem.Results {
		b := database.Results[i]
		if a.ID != b.ID {
			t.Error("case ordering differs")
			continue
		}
		if a.Status != Verified || b.Status != Verified {
			t.Errorf("%s parity NOT_VERIFIED: FS=%s DB=%s", a.ID, a.Status, b.Status)
			continue
		}
		equal(t, a.Observations, b.Observations)
	}
}
