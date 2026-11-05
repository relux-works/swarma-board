package app

import (
	"context"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/domain"
	"github.com/relux-works/swarma-board/internal/query"
)

// ConcreteSynthetic connects exactly the completed P1 constructors. Fixtures
// supply no implementation, UID mapping or production adapter.
func ConcreteSynthetic(ctx context.Context, o SyntheticOptions) (*Synthetic, error) {
	if o.Factory != nil || o.Parser != nil {
		return nil, &contract.Error{Code: "unavailable", Message: "concrete synthetic dependencies are owned by composition"}
	}
	o.Parser = query.New()
	o.Factory = func(in DomainInputs) (contract.Service, error) { return domain.New(in.Repository, in.Now, in.NewID) }
	return NewSynthetic(ctx, o)
}

func newID(kind string) (string, error) { return domain.RandomID(kind, time.Now().UTC()) }
