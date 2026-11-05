// Package app reserves explicit composition inputs for the integration owner.
// P0 defines signatures only; concrete packages are connected in P5.
package app

import (
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
)

// DomainInputs is the constructor input agreement for the P1 Service. NewID
// receives the canonical element type and returns a collision candidate;
// collision checks remain inside the domain transaction callback. Now returns
// UTC time; neither function may perform external I/O or launch work.
type DomainInputs struct {
	Repository contract.Repository
	Now        func() time.Time
	NewID      func(elementType string) (string, error)
}

// ServerInputs supplies only trusted local seams, never ambient auth/config.
type ServerInputs struct {
	Service    contract.Service
	Parser     contract.Parser
	Transfer   contract.Transfer
	Authorizer contract.Authorizer
}

// ClientInputs separates projection discovery from credentials. Production
// adapters remain held; a missing input must fail closed at composition.
type ClientInputs struct {
	Projection contract.ProjectionSource
	Credential contract.CredentialSource
}
