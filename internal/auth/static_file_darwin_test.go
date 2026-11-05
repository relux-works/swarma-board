//go:build darwin

package auth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestDarwinPolicyACLRefusalAndLatchedDrift(t *testing.T) {
	path, _ := writeStaticTestPolicy(t, staticTestPolicy())
	r, err := NewStaticPeerResolver(path, testPolicyVersion)
	if err != nil || r.Ready(context.Background()) != nil {
		t.Fatal("owner-private ACL-free policy refused", err)
	}
	// Darwin ACL grants are independent of POSIX mode bits. Install only a
	// synthetic fixture ACL and prove a still-0600 file is no longer admitted.
	if out, err := exec.Command("/bin/chmod", "+a", "everyone allow read", path).CombinedOutput(); err != nil {
		t.Fatalf("cannot establish ACL negative witness: %v: %s", err, out)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("ACL witness did not retain mode 0600", st, err)
	}
	if next, err := NewStaticPeerResolver(path, testPolicyVersion); err == nil || next != nil {
		t.Fatal("ACL-bearing policy accepted")
	}
	if !errors.Is(r.Ready(context.Background()), ErrStaticPolicyRestartRequired) {
		t.Fatal("ACL drift did not latch")
	}
	if out, err := exec.Command("/bin/chmod", "-N", path).CombinedOutput(); err != nil {
		t.Fatalf("cannot remove synthetic ACL: %v: %s", err, out)
	}
	if !errors.Is(r.Ready(context.Background()), ErrStaticPolicyRestartRequired) {
		t.Fatal("removing ACL cleared latch")
	}
	if fresh, err := NewStaticPeerResolver(path, testPolicyVersion); err != nil || fresh == nil {
		t.Fatal("fresh ACL-free policy refused", err)
	}
	if err := staticPolicyNoACL(-1); err == nil {
		t.Fatal("failed ACL query admitted")
	}
}
