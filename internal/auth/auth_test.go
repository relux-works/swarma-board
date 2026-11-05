package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
)

func fixtureData(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("../testadapter/testdata/server.json")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func decodeFixture(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var f map[string]any
	if e := json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func encode(t *testing.T, f map[string]any) []byte {
	t.Helper()
	b, e := json.Marshal(f)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestFixtureHashOnlyRolesAndCurrentRefusals(t *testing.T) {
	now := time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)
	f := decodeFixture(t, fixtureData(t))
	tokens := f["tokens"].([]any)
	// Test tokens are local synthetic values and never emitted by the implementation.
	for i, raw := range tokens {
		sum := sha256.Sum256([]byte([]string{"unit-viewer", "unit-editor", "unit-admin"}[i]))
		raw.(map[string]any)["sha256"] = hex.EncodeToString(sum[:])
	}
	a, e := New(strings.NewReader(string(encode(t, f))), func() time.Time { return now })
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	for _, tc := range []struct {
		token, role string
		ok          bool
	}{{"unit-viewer", "viewer", true}, {"unit-viewer", "editor", false}, {"unit-editor", "editor", true}, {"unit-editor", "admin", false}, {"unit-admin", "admin", true}} {
		p, e := a.Authenticate(ctx, tc.token)
		if e != nil {
			t.Fatal(e)
		}
		e = a.Require(ctx, p, "synthetic-board", tc.role)
		if (e == nil) != tc.ok {
			t.Fatal(tc.role, e)
		}
		if a.Require(ctx, p, "foreign", "viewer") == nil {
			t.Fatal("foreign project granted")
		}
	}
	if _, e = a.Authenticate(ctx, "unknown"); e == nil {
		t.Fatal("unknown token accepted")
	}
	p, _ := a.Authenticate(ctx, "unit-admin")
	if e = a.Require(ctx, p, "foreign", "viewer"); e == nil {
		t.Fatal("admin bypass")
	}
	if e = a.Require(ctx, contract.Principal{AccountID: "forged"}, "synthetic-board", "viewer"); e == nil {
		t.Fatal("forged principal")
	}
	projects, e := a.Projects(ctx, p)
	if e != nil || len(projects) != 1 {
		t.Fatal(projects, e)
	}
	for _, kind := range []string{"revoked", "disabled", "expired"} {
		copy := decodeFixture(t, encode(t, f))
		switch kind {
		case "revoked":
			copy["tokens"].([]any)[0].(map[string]any)["revoked"] = true
		case "disabled":
			copy["accounts"].([]any)[0].(map[string]any)["disabled"] = true
		case "expired":
			copy["tokens"].([]any)[0].(map[string]any)["expires_at"] = now.Format(time.RFC3339)
		}
		blocked, e := New(strings.NewReader(string(encode(t, copy))), func() time.Time { return now })
		if e != nil {
			t.Fatal(e)
		}
		if _, e = blocked.Authenticate(ctx, "unit-viewer"); e == nil {
			t.Fatal(kind, "accepted")
		}
	}
	// Require checks current account/grant maps, not a principal-carried role.
	delete(a.grants[p.AccountID], "synthetic-board")
	if a.Require(ctx, p, "synthetic-board", "viewer") == nil {
		t.Fatal("stale grant honored")
	}
}
func TestFixtureRejectsAmbiguousUnknownAndDanglingData(t *testing.T) {
	base := fixtureData(t)
	cases := map[string]func(map[string]any){
		"unknown":           func(f map[string]any) { f["authority"] = "admin" },
		"not-synthetic":     func(f map[string]any) { f["synthetic"] = false },
		"new-schema":        func(f map[string]any) { f["schema_version"] = 2 },
		"duplicate-project": func(f map[string]any) { f["projects"] = append(f["projects"].([]any), f["projects"].([]any)[0]) },
		"duplicate-account": func(f map[string]any) { f["accounts"] = append(f["accounts"].([]any), f["accounts"].([]any)[0]) },
		"duplicate-token":   func(f map[string]any) { f["tokens"] = append(f["tokens"].([]any), f["tokens"].([]any)[0]) },
		"duplicate-grant":   func(f map[string]any) { f["grants"] = append(f["grants"].([]any), f["grants"].([]any)[0]) },
		"dangling-token":    func(f map[string]any) { f["tokens"].([]any)[0].(map[string]any)["account_id"] = "missing" },
		"dangling-project":  func(f map[string]any) { f["grants"].([]any)[0].(map[string]any)["project_id"] = "missing" },
		"unknown-role":      func(f map[string]any) { f["grants"].([]any)[0].(map[string]any)["role"] = "owner" },
		"invalid-hash":      func(f map[string]any) { f["tokens"].([]any)[0].(map[string]any)["sha256"] = "raw secret" },
		"unknown-nested":    func(f map[string]any) { f["accounts"].([]any)[0].(map[string]any)["grant"] = "admin" },
		"missing-disabled":  func(f map[string]any) { delete(f["accounts"].([]any)[0].(map[string]any), "disabled") },
		"null-disabled":     func(f map[string]any) { f["accounts"].([]any)[0].(map[string]any)["disabled"] = nil },
		"case-folded-key": func(f map[string]any) {
			v := f["accounts"].([]any)[0].(map[string]any)
			v["ID"] = v["id"]
			delete(v, "id")
		},
		"path-project":  func(f map[string]any) { f["projects"].([]any)[0].(map[string]any)["id"] = "../outside" },
		"null-projects": func(f map[string]any) { f["projects"] = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := decodeFixture(t, base)
			change(f)
			if _, e := New(strings.NewReader(string(encode(t, f))), time.Now); e == nil {
				t.Fatal("accepted")
			}
		})
	}
	for _, b := range [][]byte{append(append([]byte{}, base...), []byte(" {}")...), []byte(`{"schema_version":1,"schema_version":1}`), []byte(`{"synthetic":true,"synthe\u0074ic":false}`), {0xff}} {
		if _, e := New(strings.NewReader(string(b)), time.Now); e == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}
func TestSyntheticBindRefusal(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:9090"} {
		if e := ValidateSyntheticListen(addr); e != nil {
			t.Fatal(addr, e)
		}
	}
	for _, addr := range []string{"localhost:8080", "0.0.0.0:8080", "[::]:8080", "192.0.2.1:8080", "127.0.0.1:99999", "127.0.0.1:http"} {
		if ValidateSyntheticListen(addr) == nil {
			t.Fatal(addr)
		}
	}
}
