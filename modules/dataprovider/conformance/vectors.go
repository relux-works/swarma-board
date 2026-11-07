// Package conformance owns the single immutable provider test oracle. Adapter
// factories supply storage and fault mechanisms, never expectations or tests.
package conformance

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

const (
	SpecSHA256    = "4dd57e520e9b7911c3200b5de9068847f2a451617bbe595ef236f4d6e785a13b"
	VectorsSHA256 = "9e5f34b2ea6ea1ee292aff82590c33b0d977ee6dffc3d3e7e6f4929e52184c7f"
	SourceHead    = "f403f0ed5042e79e1b718a98bec3b839eba4b78a"
	SourceTree    = "197cfb7f13fe47180f6c049da983893bddb843b6"
)

//go:embed testdata/conformance.json
var frozen []byte

type Case struct {
	ID      string   `json:"id"`
	Layer   string   `json:"layer"`
	Clauses []string `json:"clauses"`
	Action  string   `json:"action"`
	Oracle  string   `json:"oracle"`
}
type VectorSet struct {
	Cases   []Case `json:"cases"`
	Fixture struct {
		Scopes []struct {
			ProductID string `json:"product_id"`
			BoardID   string `json:"board_id"`
			Name      string `json:"name"`
		} `json:"scopes"`
		Elements []dp.Element `json:"elements"`
		Actor    dp.Actor     `json:"actor"`
	} `json:"fixture"`
}

// Vectors returns a fresh parsed copy. The exact original bytes are checked on
// every load; adapters cannot edit fixtures or suppress any of the fifty cases.
func Vectors() (VectorSet, error) {
	h := sha256.Sum256(frozen)
	if hex.EncodeToString(h[:]) != VectorsSHA256 {
		return VectorSet{}, fmt.Errorf("frozen vector digest differs")
	}
	var v VectorSet
	if err := json.Unmarshal(frozen, &v); err != nil {
		return v, err
	}
	if len(v.Cases) != 50 {
		return v, fmt.Errorf("expected fifty frozen cases")
	}
	for i, c := range v.Cases {
		if c.ID != fmt.Sprintf("C%02d", i+1) {
			return v, fmt.Errorf("case ordering differs")
		}
	}
	return v, nil
}
