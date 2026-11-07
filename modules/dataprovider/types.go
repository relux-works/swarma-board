package dataprovider

import (
	"context"
	"io"
	"time"
)

const EventFormatVersion uint32 = 1

type Scope struct {
	ProductID string `json:"product_id"`
	BoardID   string `json:"board_id"`
}
type Revision struct {
	Scope   Scope  `json:"scope"`
	Counter uint64 `json:"counter"`
}
type Board struct {
	Scope       Scope  `json:"scope"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Snapshot struct {
	Board      Board            `json:"board"`
	Revision   Revision         `json:"revision"`
	Elements   []Element        `json:"elements"`
	Provenance []ProvenanceFile `json:"provenance"`
	Resources  []ResourceInfo   `json:"resources"`
}
type ChangeSet struct {
	Upserts []Element `json:"upserts"`
	Deletes []string  `json:"deletes"`
	// Nil preserves provenance; nonnil empty replaces it with no files.
	Provenance      []ProvenanceFile `json:"provenance"`
	PutResources    []ResourceInput  `json:"-"`
	DeleteResources []string         `json:"delete_resources"`
}
type ProvenanceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"data"`
}
type ResourceInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type ResourceInput struct {
	Info ResourceInfo
	// Reader is server-local staged input, prepared before Update. It is read
	// once after the callback, never retried, closed or retained by a provider.
	Reader io.Reader
}
type ResourceReader struct {
	Info ResourceInfo
	// Reader is verified before acquisition succeeds. The caller must consume
	// through EOF and close successfully for a complete read. It pins bytes
	// across membership deletion and retains the original admission context.
	Reader io.ReadCloser
}
type Actor struct {
	AccountID string    `json:"account_id"`
	RequestID string    `json:"request_id"`
	Operation string    `json:"operation"`
	At        time.Time `json:"at"`
}
type Commit struct {
	Revision           Revision `json:"revision"`
	ChangedIDs         []string `json:"changed_ids"`
	ChangedResourceIDs []string `json:"changed_resource_ids"`
}
type Event struct {
	FormatVersion      uint32   `json:"format_version"`
	Scope              Scope    `json:"scope"`
	Revision           Revision `json:"revision"`
	Actor              Actor    `json:"actor"`
	ChangedIDs         []string `json:"changed_ids"`
	ChangedResourceIDs []string `json:"changed_resource_ids"`
}

// Checkpoint pins one revision and its complete contiguous event and resource
// catalog. Metadata is detached on every call. Only one child need be active;
// another open then returns invalid_request. Children may be reopened after
// closing. Close is idempotent, closes any child, and releases backing state.
// Closed/expired handles refuse opens/reads with unavailable (context errors
// remain errors.Is recognizable); metadata copies remain usable.
type Checkpoint interface {
	Snapshot() Snapshot
	Events() []Event
	OpenResource(string) (io.ReadCloser, error)
	Close() error
}

// Provider operates only on admitted explicit scope pairs. Update performs
// scoped CAS before calling its trusted, bounded callback at most once. Facts,
// provenance, resource membership/bytes, next revision and event have one durable
// decision. Refusals return zero success values. Ambiguity latches unavailable
// until recovery; writes are never automatically retried. Read handles count as
// in-flight until closed/expired. Close stops admission and drains within
// CloseTimeout; timeout retains live backing and a later Close can finish.
type Provider interface {
	View(context.Context, Scope) (Snapshot, error)
	ReadResource(context.Context, Scope, string) (ResourceReader, error)
	Update(context.Context, Scope, Revision, Actor, func(Snapshot) (ChangeSet, error)) (Commit, error)
	Checkpoint(context.Context, Scope) (Checkpoint, error)
	Ready(context.Context) error
	Close() error
}

// Provisioner is a server control-plane seam, never network board CRUD.
// EnsureEmpty creates an absent scope at revision zero. Identical existing
// metadata is always a no-op, including when facts exist; it does not certify
// emptiness. Different metadata returns scope_conflict without overwriting.
type Provisioner interface {
	EnsureEmpty(context.Context, Board) error
}
