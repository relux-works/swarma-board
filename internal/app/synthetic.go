package app

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/relux-works/swarma-board/internal/auth"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/httpapi"
	"github.com/relux-works/swarma-board/internal/registry"
	"github.com/relux-works/swarma-board/internal/transfer"
)

// SyntheticOptions never discovers configuration or enables production mode.
// Factory and Parser are supplied by concrete P1 composition, not fixture data.
type SyntheticOptions struct {
	Fixture                 io.Reader
	DataDir, Listen, Commit string
	Factory                 func(DomainInputs) (contract.Service, error)
	Parser                  contract.Parser
}

type Synthetic struct {
	Registry *registry.Registry
	Service  contract.Service
	Handler  *httpapi.Handler
	Server   *http.Server
}

// NewSynthetic validates authority and listen policy before touching storage.
// It never opens a listener; Host/test composition owns Serve and Shutdown.
func NewSynthetic(ctx context.Context, o SyntheticOptions) (*Synthetic, error) {
	if o.Fixture == nil || o.DataDir == "" || o.Factory == nil || o.Parser == nil {
		return nil, &contract.Error{Code: "unavailable", Message: "explicit synthetic dependencies required"}
	}
	if err := auth.ValidateSyntheticListen(o.Listen); err != nil {
		return nil, err
	}
	a, err := auth.New(o.Fixture, time.Now)
	if err != nil {
		return nil, err
	}
	r, err := registry.New(o.DataDir)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = r.Close()
		}
	}()
	if err = r.Provision(ctx, a.ProvisionProjects()); err != nil {
		return nil, err
	}
	service, err := o.Factory(DomainInputs{Repository: r, Now: time.Now, NewID: newID})
	if err != nil {
		return nil, err
	}
	h, err := httpapi.New(httpapi.Options{Service: service, Parser: o.Parser, Transfer: transfer.New(o.Commit), Authorizer: a, StorageReady: r.Ready, Commit: o.Commit})
	if err != nil {
		return nil, err
	}
	s, err := h.Server(o.Listen)
	if err != nil {
		return nil, err
	}
	if err = r.Ready(ctx); err != nil {
		return nil, err
	}
	ok = true
	return &Synthetic{Registry: r, Service: service, Handler: h, Server: s}, nil
}

// Close is called after HTTP Shutdown has drained admitted requests.
func (s *Synthetic) Close() error { return s.Registry.Close() }
