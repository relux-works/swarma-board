// Package contract defines the local swarma-board v1 composition seams.
// These are product proposal interfaces, not an upstream platform contract.
package contract

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

type Revision uint64
type Projection struct{ Endpoint, ProjectID, AccountID string }
type ProjectionSource interface {
	Resolve(context.Context) (Projection, error)
}
type CredentialSource interface {
	Bearer(context.Context, Projection) (string, error)
}
type Principal struct{ AccountID string }
type Authorizer interface {
	Authenticate(context.Context, string) (Principal, error)
	Require(context.Context, Principal, string, string) error // project, viewer|editor|admin
	Projects(context.Context, Principal) ([]Project, error)
	Ready(context.Context) error
}
type Project struct{ ID, Name, Description string }
type Operation struct {
	Name   string
	Args   map[string]json.RawMessage // decoded typed values; id/scope positional normalized to names
	Fields []string                   // expanded, validated selector names
}
type Parser interface {
	Parse(string) (Operation, error)
}
type AuditContext struct {
	AccountID, RequestID, Operation string
	At                              time.Time
}
type Snapshot struct {
	Project    Project
	Revision   Revision
	Elements   []Element
	Provenance []ProvenanceFile
}
type ChangeSet struct {
	Upserts    []Element
	Deletes    []string
	Provenance []ProvenanceFile // import-only replacement; ordinary mutations leave nil
}
type Commit struct {
	Revision   Revision
	ChangedIDs []string
}
type Repository interface {
	View(context.Context, string) (Snapshot, error)
	Update(context.Context, string, Revision, AuditContext,
		func(Snapshot) (ChangeSet, error)) (Commit, error)
	Ready(context.Context) error
	Close() error
}
type Call struct {
	ProjectID string
	Operation Operation
	Expected  *Revision    // required on writes, nil on reads
	Audit     AuditContext // authenticated account and server-generated time/request ID
}
type Reply struct {
	Revision Revision
	JSON     json.RawMessage
}
type PreparedImport struct {
	SHA256   string
	Snapshot Snapshot
	Report   ImportReport
}
type ImportOptions struct{ ProjectID, ExpectedSHA256 string }
type ImportReport struct {
	SHA256, Format string
	Version        int
	ElementCount   int
	Exclusions     []string
	Warnings       []string
}
type Transfer interface {
	Prepare(context.Context, io.Reader, ImportOptions) (PreparedImport, error)
	Encode(context.Context, io.Writer, Snapshot) error
}
type Service interface {
	Execute(context.Context, Call) (Reply, error)
	Snapshot(context.Context, string) (Snapshot, error)
	Import(context.Context, string, Revision, AuditContext, PreparedImport) (Commit, error)
}
type ProvenanceFile struct {
	Path, SHA256 string
	Data         []byte
}
type Error struct {
	Code, Message string
	Details       map[string]any
}

// Error implements error; no HTTP dependencies. Status/exit mappings belong to adapters.
