//go:build linux

package fs

import (
	"context"
	"io"
	"os"
	"sync"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

type pinnedReader struct {
	io.Reader
	p       *Provider
	ctx     context.Context
	once    sync.Once
	err     error
	exposed bool
}

func (r *pinnedReader) Read(b []byte) (int, error) {
	if r.exposed {
		if err := r.p.hit(r.ctx, "resource_read"); err != nil {
			return 0, err
		}
	}
	return r.Reader.Read(b)
}
func (r *pinnedReader) Close() error {
	r.once.Do(func() {
		if r.exposed {
			r.err = r.p.hit(context.Background(), "resource_close")
		}
	})
	return r.err
}

// Backing pins read-only file descriptors to this exact generation. Reopening a
// child uses a new section reader over the same immutable inode, never CURRENT.
func (p *Provider) backing(ctx context.Context, state diskState, infos []dp.ResourceInfo, end func(), cancel context.CancelFunc) (dp.ResourceBacking, error) {
	files := map[string]*os.File{}
	release := func() error {
		var err error
		for _, f := range files {
			if ce := f.Close(); err == nil {
				err = storage(ce)
			}
		}
		cancel()
		end()
		return err
	}
	for _, info := range infos {
		f, err := p.safeFile(state.Dir + "/resources/" + info.SHA256)
		if err != nil {
			_ = release()
			return dp.ResourceBacking{}, err
		}
		files[info.SHA256] = f
	}
	var once sync.Once
	var releaseErr error
	// First open is acquisition verification, subsequent opens are exposed.
	mu := sync.Mutex{}
	opens := map[string]int{}
	return dp.ResourceBacking{Open: func(d string) (io.ReadCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, dp.ContextError(err)
		}
		mu.Lock()
		defer mu.Unlock()
		f := files[d]
		if f == nil {
			return nil, fail(dp.NotFound)
		}
		st, err := f.Stat()
		if err != nil {
			return nil, storage(err)
		}
		opens[d]++
		return &pinnedReader{Reader: io.NewSectionReader(f, 0, st.Size()), p: p, ctx: ctx, exposed: opens[d] > 1}, nil
	}, Release: func() error { once.Do(func() { releaseErr = release() }); return releaseErr }}, nil
}
func (p *Provider) ReadResource(parent context.Context, s dp.Scope, d string) (dp.ResourceReader, error) {
	ctx, cancel := admitStream(parent, dp.ReadTimeout)
	if err := dp.ValidateDigest(d); err != nil {
		cancel()
		return dp.ResourceReader{}, err
	}
	if err := dp.ValidateScope(s); err != nil {
		cancel()
		return dp.ResourceReader{}, err
	}
	end, err := p.begin(ctx)
	if err != nil {
		cancel()
		return dp.ResourceReader{}, err
	}
	unlock, err := p.lock(ctx, s)
	if err != nil {
		cancel()
		end()
		return dp.ResourceReader{}, err
	}
	state, err := p.load(ctx, s, true)
	if err != nil {
		unlock()
		cancel()
		end()
		return dp.ResourceReader{}, err
	}
	var info dp.ResourceInfo
	found := false
	for _, r := range state.Snapshot.Resources {
		if r.SHA256 == d {
			info = r
			found = true
			break
		}
	}
	if !found {
		unlock()
		cancel()
		end()
		return dp.ResourceReader{}, fail(dp.NotFound)
	}
	backing, err := p.backing(ctx, state, []dp.ResourceInfo{info}, end, cancel)
	unlock()
	if err != nil {
		return dp.ResourceReader{}, err
	}
	return dp.OpenResourceReader(ctx, info, backing)
}
func (p *Provider) Checkpoint(parent context.Context, s dp.Scope) (dp.Checkpoint, error) {
	ctx, cancel := admitStream(parent, dp.CheckpointTimeout)
	if err := dp.ValidateScope(s); err != nil {
		cancel()
		return nil, err
	}
	end, err := p.begin(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	unlock, err := p.lock(ctx, s)
	if err != nil {
		cancel()
		end()
		return nil, err
	}
	state, err := p.load(ctx, s, true)
	if err != nil {
		unlock()
		cancel()
		end()
		return nil, err
	}
	backing, err := p.backing(ctx, state, state.Snapshot.Resources, end, cancel)
	unlock()
	if err != nil {
		return nil, err
	}
	return dp.OpenCheckpoint(ctx, state.Snapshot, state.Events, backing)
}
