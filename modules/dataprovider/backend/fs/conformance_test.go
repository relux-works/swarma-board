//go:build linux

package fs

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	dp "github.com/relux-works/swarma-board/dataprovider"
	"github.com/relux-works/swarma-board/dataprovider/conformance"
)

func localFactory() conformance.Factory {
	return conformance.Factory{Name: "filesystem", Open: func(ctx context.Context, root string) (*conformance.Instance, error) {
		p, err := Open(ctx, root)
		if err != nil {
			return nil, err
		}
		var mu sync.Mutex
		instance := &conformance.Instance{Provider: p, Provisioner: p}
		instance.Restart = func(ctx context.Context) (dp.Provider, dp.Provisioner, error) {
			mu.Lock()
			defer mu.Unlock()
			_ = p.Close()
			next, err := Open(ctx, root)
			if err != nil {
				return nil, nil, err
			}
			p = next
			return p, p, nil
		}
		instance.OpenPeer = func(ctx context.Context) (dp.Provider, dp.Provisioner, func() error, error) {
			peer, err := Open(ctx, root)
			if err != nil {
				return nil, nil, nil, err
			}
			return peer, peer, peer.Close, nil
		}
		instance.Cleanup = func() error { mu.Lock(); defer mu.Unlock(); return p.Close() }
		return instance, nil
	}}
}
func TestConformance(t *testing.T) {
	d := newDriver()
	report := conformance.Run(t, d.factory(), d)
	if name := os.Getenv("FS_REPORT"); name != "" {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
