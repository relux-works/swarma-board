package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/relux-works/swarma-board/internal/deployment"
)

func TestDeploymentDispatchRefusesAmbiguityBeforeWrites(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	if err := os.Mkdir(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	base := []string{"--deployment-profile=single-trust-unit", "--unix-socket=" + filepath.Join(runtime, "s"), "--data-dir=" + data, "--policy-file=" + filepath.Join(root, "policy.json"), "--audit-export-dir=" + filepath.Join(root, "exports"), "--policy-version=synthetic-v1"}
	cases := [][]string{base[1:], base[:len(base)-1]}
	for _, extra := range []string{"--listen=127.0.0.1:0", "--tls-cert=missing", "--tls-key=missing", "--remote-tls", "--remote-tls=false", "--synthetic-fixture=missing", "--deployment-status", "--policy-version=other", "--unix-socket=", "positional"} {
		cases = append(cases, append(append([]string{}, base...), extra))
	}
	for _, args := range cases {
		var out, notices bytes.Buffer
		if code := RunServerWithOutput(context.Background(), args, "dev", &out, &notices); code != 2 {
			t.Fatalf("%v: exit %d: %s", args, code, &notices)
		}
		if _, err := os.Lstat(data); !os.IsNotExist(err) {
			t.Fatal("refusal created data", err)
		}
	}
	// A valid config with an absent policy still refuses before storage creation.
	var out bytes.Buffer
	if code := RunServer(context.Background(), base, "dev", &out); code != 1 {
		t.Fatalf("missing policy: %d: %s", code, &out)
	}
	if _, err := os.Lstat(data); !os.IsNotExist(err) {
		t.Fatal("missing policy created data", err)
	}
}

func TestDeploymentStatusUsesDedicatedOutput(t *testing.T) {
	var status, notices bytes.Buffer
	args := []string{"--deployment-profile=single-trust-unit", "--deployment-status"}
	if n := RunServerWithOutput(context.Background(), args, "dev", &status, &notices); n != 0 {
		t.Fatal(n, &notices)
	}
	var got deployment.Descriptor
	if err := json.Unmarshal(status.Bytes(), &got); err != nil || got.Profile != deployment.SingleTrustUnit || got.ProductionAdmitted || notices.Len() != 0 {
		t.Fatal(got, err, &notices)
	}
	for _, args := range [][]string{
		{"--deployment-profile=", "--deployment-profile=single-trust-unit", "--deployment-status"},
		{"--deployment-profile=single-trust-unit", "--deployment-status", "--synthetic-credential-fd=3"},
		{"--deployment-profile=single-trust-unit", "--deployment-projection="},
	} {
		status.Reset()
		notices.Reset()
		if n := RunClient(context.Background(), args, "dev", &status, &notices); n != 2 {
			t.Fatal(args, n, &status, &notices)
		}
	}
}
