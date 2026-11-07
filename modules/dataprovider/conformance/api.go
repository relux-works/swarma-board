package conformance

import (
	"context"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

// Factory opens only the runner's new private absolute root. A process-backed
// Provider proxy is permitted, enabling real process kill/restart testing.
// No environment/home discovery, network, production DSN, or real data is used.
type Factory struct {
	Name string // exactly filesystem or database
	Open func(context.Context, string) (*Instance, error)
}
type Instance struct {
	Provider    dp.Provider
	Provisioner dp.Provisioner
	// Restart reopens/reconciles the same state, including after process death.
	// It returns new handles; the old process must no longer admit operations.
	Restart func(context.Context) (dp.Provider, dp.Provisioner, error)
	// OpenPeer opens a distinct real handle/process against the same root.
	// A finite owner-lock unavailable refusal is a conforming alternative.
	OpenPeer func(context.Context) (dp.Provider, dp.Provisioner, func() error, error)
	// Cleanup terminates test-owned children and releases state on every path.
	Cleanup func() error
}

type Stage string

const (
	BeforeDecision      Stage = "before_decision"
	AfterDecision       Stage = "after_decision_before_ack"
	DuringRecovery      Stage = "during_recovery"
	DuringResourceRead  Stage = "during_exposed_resource_read"
	DuringResourceClose Stage = "during_exposed_resource_close"
	DuringProviderClose Stage = "during_provider_final_close"
)

type FaultMode string

const (
	Pause                FaultMode = "pause"
	DefiniteFailure      FaultMode = "definite_failure"
	Kill                 FaultMode = "kill"
	AmbiguousFailure     FaultMode = "ambiguous_failure"
	DiskFull             FaultMode = "disk_full_before_decision"
	ReadFailure          FaultMode = "late_resource_read_failure"
	CloseFailure         FaultMode = "late_resource_close_failure"
	ProviderCloseFailure FaultMode = "provider_final_close_failure"
)

type FaultPoint struct {
	ID    string `json:"id"`
	Stage Stage  `json:"stage"`
	// Physical is a written mapping to a real durable engine/FS point.
	Physical string `json:"physical"`
	// FSPhase identifies journal_sync, first_write, last_write, rename,
	// resource_payload, resource_membership, revision_event, parent_sync, cleanup.
	FSPhase string `json:"fs_phase,omitempty"`
}
type FaultReceipt struct {
	PointID          string    `json:"point_id"`
	ReachedAt        time.Time `json:"reached_at"`
	TriggeredAt      time.Time `json:"triggered_at"`
	Mode             FaultMode `json:"mode"`
	ProcessExit      *int      `json:"process_exit,omitempty"`
	EvidenceRetained bool      `json:"evidence_retained"`
}

// A ticket pauses the real worker at its mapped point. WaitReached must be
// bounded by ctx. Trigger applies the armed mode (or resumes for Pause), then
// returns actual clock/exit/evidence. Close releases/terminates on all paths.
type FaultTicket interface {
	WaitReached(context.Context) error
	Trigger(context.Context) (FaultReceipt, error)
	Close() error
}
type Corruption string

const (
	HistoryGap          Corruption = "history_gap"
	HistoryDuplicate    Corruption = "history_duplicate"
	HistoryTail         Corruption = "history_tail_overwrite"
	HistoryScope        Corruption = "history_scope"
	HistoryRevision     Corruption = "history_revision"
	HistoryOrdering     Corruption = "history_changed_id_order"
	UnknownSchema       Corruption = "unknown_schema"
	MissingSchema       Corruption = "missing_schema"
	ScopeMetadata       Corruption = "scope_metadata"
	RecoveryEvidence    Corruption = "ambiguous_recovery_evidence"
	PayloadBytes        Corruption = "resource_bytes"
	PayloadDigest       Corruption = "resource_digest"
	PayloadSize         Corruption = "resource_size"
	PayloadCatalog      Corruption = "resource_catalog"
	OutsideRootAlias    Corruption = "outside_root_alias"
	UnreadableFacts     Corruption = "unreadable_facts"
	UnknownMarkdown     Corruption = "unrepresentable_markdown"
	ProgressOnly        Corruption = "progress_without_readme"
	LegacyTree          Corruption = "legacy_no_schema_tree"
	ExternalWriter      Corruption = "concurrent_external_writer"
	AuthorityDependency Corruption = "execution_authority_dependency"
)

// FaultDriver supplies mechanisms only. It is never allowed to assert an oracle
// or return a case verdict. Corrupt and InstallRevision operate on stopped,
// disposable test state; they are not provider or server capabilities.
type FaultDriver interface {
	Points() []FaultPoint
	Arm(context.Context, string, FaultPoint, FaultMode) (FaultTicket, error)
	Corrupt(context.Context, string, dp.Scope, Corruption) error
	InstallRevision(context.Context, string, dp.Scope, uint64) error
}

// HistoryFixture is a complete synthetic committed state, not a request to drop
// continuity. Emit yields every exact Event in ascending order with a bounded
// generator; an installer may stream it to real storage. The snapshot's counter
// equals the emitted count and NDJSONBytes includes every LF. Resource-free
// fixtures preserve the ordinary seeded facts and inert synthetic history.
type HistoryFixture struct {
	Snapshot    dp.Snapshot
	NDJSONBytes int64
	Emit        func(func(dp.Event) error) error
}

// HistoryDriver is an additional stopped-state fixture mechanism needed for
// reachable history-cap cases. It installs the supplied exact facts/history,
// never an expected verdict or an adapter-selected weaker limit.
type HistoryDriver interface {
	InstallHistory(context.Context, string, HistoryFixture) error
}

type Status string

const (
	Verified    Status = "VERIFIED"
	Failed      Status = "FAILED"
	NotVerified Status = "NOT_VERIFIED"
)

type Result struct {
	ID           string                 `json:"id"`
	Layer        string                 `json:"layer"`
	Status       Status                 `json:"status"`
	Detail       string                 `json:"detail"`
	Started      time.Time              `json:"started"`
	Elapsed      time.Duration          `json:"elapsed_ns"`
	Faults       []FaultReceipt         `json:"faults"`
	Observations []CanonicalObservation `json:"observations"`
}

// CanonicalObservation hashes the complete fixed-revision P6 value, including
// every field, event Actor and payload byte. Physical paths/timing never enter
// this value. It is not a storage attestation or an authenticated-history claim.
type CanonicalObservation struct {
	Scope    dp.Scope    `json:"scope"`
	Revision dp.Revision `json:"revision"`
	SHA256   string      `json:"checkpoint_sha256"`
	Bytes    int64       `json:"checkpoint_bytes"`
}
type Report struct {
	Provider      string   `json:"provider"`
	SpecSHA256    string   `json:"spec_sha256"`
	VectorsSHA256 string   `json:"vectors_sha256"`
	Results       []Result `json:"results"`
}

func (r Report) AllVerified() bool {
	if len(r.Results) != 50 {
		return false
	}
	for _, c := range r.Results {
		if c.Status != Verified {
			return false
		}
	}
	return true
}
