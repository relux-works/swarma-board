package conformance

import (
	"fmt"
	dp "github.com/relux-works/swarma-board/dataprovider"
)

func observationLess(a, b CanonicalObservation) bool {
	if a.Scope.ProductID != b.Scope.ProductID {
		return a.Scope.ProductID < b.Scope.ProductID
	}
	if a.Scope.BoardID != b.Scope.BoardID {
		return a.Scope.BoardID < b.Scope.BoardID
	}
	if a.Revision.Counter != b.Revision.Counter {
		return a.Revision.Counter < b.Revision.Counter
	}
	if a.SHA256 != b.SHA256 {
		return a.SHA256 < b.SHA256
	}
	return a.Bytes < b.Bytes
}

// verificationError is the single fail-closed gate for local and paired reports.
// Canonical observations are required for every verified provider case, including
// observations from its disposable subcases. Other layers retain their frozen
// integration/qualification obligations; this structural check is no attestation.
func (r Report) verificationError() error {
	if r.Provider != "filesystem" && r.Provider != "database" {
		return fmt.Errorf("report requires a real adapter name")
	}
	if r.SpecSHA256 != SpecSHA256 || r.VectorsSHA256 != VectorsSHA256 {
		return fmt.Errorf("report requires frozen spec/vector digests")
	}
	v, err := Vectors()
	if err != nil {
		return err
	}
	if len(r.Results) != len(v.Cases) {
		return fmt.Errorf("incomplete fifty-case report")
	}
	for i, c := range r.Results {
		frozen := v.Cases[i]
		if c.ID != frozen.ID || c.Layer != frozen.Layer {
			return fmt.Errorf("report row %d requires %s layer %s", i+1, frozen.ID, frozen.Layer)
		}
		if c.Status != Verified || c.Detail != "" {
			return fmt.Errorf("%s %s: %s", c.ID, c.Status, c.Detail)
		}
		scopes := map[dp.Scope]bool{}
		for j, o := range c.Observations {
			if dp.ValidateScope(o.Scope) != nil || o.Revision.Scope != o.Scope || dp.ValidateDigest(o.SHA256) != nil || o.Bytes <= 0 || o.Bytes > dp.MaxCheckpointBytes {
				return fmt.Errorf("%s invalid canonical observation", c.ID)
			}
			if j > 0 && !observationLess(c.Observations[j-1], o) {
				return fmt.Errorf("%s observations must be ordered and distinct", c.ID)
			}
			scopes[o.Scope] = true
		}
		if c.Layer == "provider" {
			for _, s := range v.Fixture.Scopes {
				if !scopes[dp.Scope{ProductID: s.ProductID, BoardID: s.BoardID}] {
					return fmt.Errorf("%s missing required scoped observations", c.ID)
				}
			}
		}
	}
	return nil
}
