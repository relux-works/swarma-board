package testadapter

import (
	"context"
	"io"

	"github.com/relux-works/swarma-board/internal/contract"
)

// Projection is an explicitly injected synthetic projection. Endpoint validation
// is the client's responsibility; this helper performs no discovery or I/O.
type Projection struct{ Value contract.Projection }

func (p Projection) Resolve(ctx context.Context) (contract.Projection, error) {
	if err := ctx.Err(); err != nil {
		return contract.Projection{}, err
	}
	return p.Value, nil
}

type ParserFunc func(string) (contract.Operation, error)

func (f ParserFunc) Parse(q string) (contract.Operation, error) {
	if f == nil {
		return contract.Operation{}, unconfigured()
	}
	return f(q)
}

// Repository supplies test hooks, not a storage implementation. Tests choose
// their snapshots/CAS/fault behavior explicitly. It makes no durability claim.
type Repository struct {
	ViewFunc   func(context.Context, string) (contract.Snapshot, error)
	UpdateFunc func(context.Context, string, contract.Revision, contract.AuditContext, func(contract.Snapshot) (contract.ChangeSet, error)) (contract.Commit, error)
	ReadyFunc  func(context.Context) error
	CloseFunc  func() error
}

func (r Repository) View(ctx context.Context, project string) (contract.Snapshot, error) {
	if r.ViewFunc == nil {
		return contract.Snapshot{}, unconfigured()
	}
	return r.ViewFunc(ctx, project)
}
func (r Repository) Update(ctx context.Context, project string, rev contract.Revision, audit contract.AuditContext, change func(contract.Snapshot) (contract.ChangeSet, error)) (contract.Commit, error) {
	if r.UpdateFunc == nil {
		return contract.Commit{}, unconfigured()
	}
	return r.UpdateFunc(ctx, project, rev, audit, change)
}
func (r Repository) Ready(ctx context.Context) error {
	if r.ReadyFunc == nil {
		return unconfigured()
	}
	return r.ReadyFunc(ctx)
}
func (r Repository) Close() error {
	if r.CloseFunc == nil {
		return unconfigured()
	}
	return r.CloseFunc()
}

type Service struct {
	ExecuteFunc  func(context.Context, contract.Call) (contract.Reply, error)
	SnapshotFunc func(context.Context, string) (contract.Snapshot, error)
	ImportFunc   func(context.Context, string, contract.Revision, contract.AuditContext, contract.PreparedImport) (contract.Commit, error)
}

func (s Service) Execute(ctx context.Context, call contract.Call) (contract.Reply, error) {
	if s.ExecuteFunc == nil {
		return contract.Reply{}, unconfigured()
	}
	return s.ExecuteFunc(ctx, call)
}
func (s Service) Snapshot(ctx context.Context, project string) (contract.Snapshot, error) {
	if s.SnapshotFunc == nil {
		return contract.Snapshot{}, unconfigured()
	}
	return s.SnapshotFunc(ctx, project)
}
func (s Service) Import(ctx context.Context, project string, rev contract.Revision, audit contract.AuditContext, prepared contract.PreparedImport) (contract.Commit, error) {
	if s.ImportFunc == nil {
		return contract.Commit{}, unconfigured()
	}
	return s.ImportFunc(ctx, project, rev, audit, prepared)
}

type Transfer struct {
	PrepareFunc func(context.Context, io.Reader, contract.ImportOptions) (contract.PreparedImport, error)
	EncodeFunc  func(context.Context, io.Writer, contract.Snapshot) error
}

func (t Transfer) Prepare(ctx context.Context, in io.Reader, options contract.ImportOptions) (contract.PreparedImport, error) {
	if t.PrepareFunc == nil {
		return contract.PreparedImport{}, unconfigured()
	}
	return t.PrepareFunc(ctx, in, options)
}
func (t Transfer) Encode(ctx context.Context, out io.Writer, snapshot contract.Snapshot) error {
	if t.EncodeFunc == nil {
		return unconfigured()
	}
	return t.EncodeFunc(ctx, out, snapshot)
}

// Authorizer is a test double. P2 owns actual fixture validation/auth logic.
type Authorizer struct {
	AuthenticateFunc func(context.Context, string) (contract.Principal, error)
	RequireFunc      func(context.Context, contract.Principal, string, string) error
	ProjectsFunc     func(context.Context, contract.Principal) ([]contract.Project, error)
	ReadyFunc        func(context.Context) error
}

func (a Authorizer) Authenticate(ctx context.Context, bearer string) (contract.Principal, error) {
	if a.AuthenticateFunc == nil {
		return contract.Principal{}, unconfigured()
	}
	return a.AuthenticateFunc(ctx, bearer)
}
func (a Authorizer) Require(ctx context.Context, principal contract.Principal, project, role string) error {
	if a.RequireFunc == nil {
		return unconfigured()
	}
	return a.RequireFunc(ctx, principal, project, role)
}
func (a Authorizer) Projects(ctx context.Context, principal contract.Principal) ([]contract.Project, error) {
	if a.ProjectsFunc == nil {
		return nil, unconfigured()
	}
	return a.ProjectsFunc(ctx, principal)
}
func (a Authorizer) Ready(ctx context.Context) error {
	if a.ReadyFunc == nil {
		return unconfigured()
	}
	return a.ReadyFunc(ctx)
}
func unconfigured() error {
	return &contract.Error{Code: "unavailable", Message: "synthetic test hook is not configured"}
}

var (
	_ contract.ProjectionSource = Projection{}
	_ contract.Parser           = ParserFunc(nil)
	_ contract.Repository       = Repository{}
	_ contract.Service          = Service{}
	_ contract.Transfer         = Transfer{}
	_ contract.Authorizer       = Authorizer{}
)
