package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/registry"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

func options(t *testing.T) SyntheticOptions {
	t.Helper()
	f, e := os.ReadFile("../testadapter/testdata/server.json")
	if e != nil {
		t.Fatal(e)
	}
	return SyntheticOptions{Fixture: bytes.NewReader(f), Listen: "127.0.0.1:0", DataDir: filepath.Join(t.TempDir(), "data"), Parser: testadapter.ParserFunc(func(string) (contract.Operation, error) { return contract.Operation{}, errors.New("unused") }), Factory: func(DomainInputs) (contract.Service, error) { return testadapter.Service{}, nil }}
}

func TestStartupPolicyPrecedesStorageAndFailureReleasesOwnership(t *testing.T) {
	for _, mutate := range []func(*SyntheticOptions){func(o *SyntheticOptions) { o.Listen = "0.0.0.0:0" }, func(o *SyntheticOptions) { o.Listen = "localhost:0" }, func(o *SyntheticOptions) { o.Fixture = strings.NewReader(`{}`) }, func(o *SyntheticOptions) { o.Factory = nil }} {
		o := options(t)
		mutate(&o)
		if s, e := NewSynthetic(context.Background(), o); e == nil {
			if s != nil {
				s.Close()
			}
			t.Fatal("startup accepted invalid inputs")
		}
		if _, e := os.Stat(o.DataDir); !os.IsNotExist(e) {
			t.Fatalf("invalid startup touched storage: %v", e)
		}
	}
	o := options(t)
	o.Factory = func(DomainInputs) (contract.Service, error) { return nil, errors.New("domain constructor refused") }
	if _, e := NewSynthetic(context.Background(), o); e == nil {
		t.Fatal("failed constructor accepted")
	}
	r, e := registry.New(o.DataDir)
	if e != nil {
		t.Fatalf("failed constructor leaked ownership lock: %v", e)
	}
	r.Close()
}

type notice chan string

func (n notice) Write(p []byte) (int, error) {
	n <- string(append([]byte(nil), p...))
	return len(p), nil
}

func TestExplicitServerHarnessShutdownDrainsAndClosesRegistry(t *testing.T) {
	o := options(t)
	file := filepath.Join(t.TempDir(), "fixture.json")
	b, e := io.ReadAll(o.Fixture)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(file, b, 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(notice, 4)
	done := make(chan int, 1)
	compose := func(ctx context.Context, in SyntheticOptions) (*Synthetic, error) {
		in.Factory = o.Factory
		in.Parser = o.Parser
		return NewSynthetic(ctx, in)
	}
	go func() {
		done <- RunSyntheticServer(ctx, []string{"--synthetic-fixture", file, "--listen", "127.0.0.1:0", "--data-dir", o.DataDir}, "dev", events, compose)
	}()
	select {
	case msg := <-events:
		if !strings.HasPrefix(msg, "synthetic fixture mode; listening 127.0.0.1:") {
			t.Fatal(msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not report startup")
	}
	cancel()
	select {
	case n := <-done:
		if n != 0 {
			t.Fatalf("shutdown exit %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown stuck")
	}
	r, e := registry.New(o.DataDir)
	if e != nil {
		t.Fatalf("shutdown leaked lock: %v", e)
	}
	r.Close()
}
