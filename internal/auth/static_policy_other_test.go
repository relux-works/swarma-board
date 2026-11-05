//go:build !linux && !darwin

package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticPolicyUnqualifiedPlatformRefuses(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	data := []byte(fmt.Sprintf(`{"schema_version":1,"policy_version":"release-1","mode":"sharedUID","trust_profile":"single-trust-unit","remote_tls":false,"projects":[],"accounts":[{"id":"test-unit","disabled":false}],"uid_mappings":[{"uid":%d,"account_id":"test-unit"}],"grants":[]}`, os.Geteuid()))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if r, e := NewStaticPeerResolver(path, "release-1"); e == nil || r != nil {
		t.Fatal("unqualified platform accepted static authority")
	}
}
