//go:build !linux

package auth

import "testing"

func TestStaticPolicyUnqualifiedPlatformRefuses(t *testing.T) {
	if r, e := NewStaticPeerResolver("/absolute/policy.json", "release-1"); e == nil || r != nil {
		t.Fatal("unqualified platform accepted static authority")
	}
}
