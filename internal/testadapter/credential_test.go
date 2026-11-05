package testadapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

func TestExplicitCredentialBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"synthetic", "swarma-board-synthetic-admin", true},
		{"one-lf", "swarma-board-synthetic-admin\n", true},
		{"bound", strings.Repeat("a", 4096), true},
		{"bound-with-lf", strings.Repeat("a", 4095) + "\n", true},
		{"overflow", strings.Repeat("a", 4096) + "\n", false},
		{"empty", "", false},
		{"empty-line", "\n", false},
		{"multiple-lines", "synthetic\nother\n", false},
		{"space", "synthetic value", false},
		{"nul", "synthetic\x00", false},
		{"invalid-utf8", "synthetic\xff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "explicit-synthetic-fd")
			if err := os.WriteFile(path, []byte(tc.value), 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			credentials, err := CredentialFromFile(context.Background(), file)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if _, err := file.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
				t.Fatal("explicit input was not closed")
			}
			if tc.valid {
				value, err := credentials.Bearer(context.Background(), contract.Projection{})
				if err != nil || value != strings.TrimSuffix(tc.value, "\n") {
					t.Fatal("credential did not survive fd consumption")
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := credentials.Bearer(ctx, contract.Projection{}); !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation ignored")
				}
			} else if credentials != nil {
				t.Fatal("invalid input returned credentials")
			}
		})
	}
}

func TestUnconfiguredAuthAndStorageFailClosed(t *testing.T) {
	ctx := context.Background()
	if err := (Authorizer{}).Ready(ctx); err == nil {
		t.Fatal("unconfigured auth ready")
	}
	if _, err := (Authorizer{}).Authenticate(ctx, "synthetic"); err == nil {
		t.Fatal("unconfigured auth accepted")
	}
	if _, err := (Repository{}).View(ctx, "synthetic-board"); err == nil {
		t.Fatal("unconfigured repository succeeded")
	}
	called := false
	_, err := (Repository{}).Update(ctx, "synthetic-board", 0, contract.AuditContext{}, func(contract.Snapshot) (contract.ChangeSet, error) {
		called = true
		return contract.ChangeSet{}, nil
	})
	if err == nil || called {
		t.Fatal("unconfigured repository invoked mutation callback")
	}
}
