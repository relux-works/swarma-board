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
	for _, r := range []Report{filesystem, database} {
		if err := r.verificationError(); err != nil {
			t.Errorf("%s: %v", r.Provider, err)
			return
		}
	}
	for i, a := range filesystem.Results {
		b := database.Results[i]
		equal(t, a.Observations, b.Observations)
	}
}
