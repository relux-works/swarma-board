// Package deployment describes and validates the explicitly selected board
// dogfood mode. It grants no authority, opens no storage/listener, and launches
// no process. Production admission is deliberately unavailable in this unit.
package deployment

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
)

const SingleTrustUnit = "single-trust-unit"

// DescriptorHeader carries the same safe JSON value as local deployment status.
// It supplements, and never replaces, the HTTP handler's readiness decision.
const DescriptorHeader = "Swarma-Board-Deployment"

var (
	ErrProfileRequired = errors.New("deployment: explicit profile required")
	ErrUnknownProfile  = errors.New("deployment: unknown profile")
	ErrProductionGated = errors.New("deployment: production refused; O/S/C principals and UID-helper, and protected per-request policy publisher remain required")
	ErrConfig          = errors.New("deployment: invalid or overlapping deployment configuration")
)

// Descriptor contains no paths, UIDs, accounts, project names, credentials or
// policy contents. Readiness is operational only; it is never production admission.
type Descriptor struct {
	Profile                         string   `json:"profile"`
	Use                             string   `json:"use"`
	Transport                       string   `json:"transport"`
	Identity                        string   `json:"identity"`
	PerAgentIsolation               bool     `json:"per_agent_isolation"`
	ProductionAdmitted              bool     `json:"production_admitted"`
	ProductionBlockers              []string `json:"production_blockers"`
	Policy                          string   `json:"policy"`
	LifecycleOwner                  string   `json:"lifecycle_owner"`
	ClientSelfSpawn                 bool     `json:"client_self_spawn"`
	ClientAutoRecovery              bool     `json:"client_auto_recovery"`
	RemoteTLSQualificationSupported bool     `json:"remote_tls_qualification_supported"`
	RemoteTLSActivation             bool     `json:"remote_tls_activation"`
}

// Describe requires exact, explicit selection. There is no production enum,
// environment default, "gates passed" switch, or caller-supplied admission bit.
func Describe(profile string) (Descriptor, error) {
	switch profile {
	case "":
		return Descriptor{}, ErrProfileRequired
	case "production":
		return Descriptor{}, ErrProductionGated
	case SingleTrustUnit:
		return Descriptor{
			Profile: SingleTrustUnit, Use: "first-dogfood", Transport: "uds-only",
			Identity:                        "shared-agent-uid; no per-agent isolation",
			ProductionBlockers:              []string{"O/S/C principals and UID-helper", "protected per-request policy publisher"},
			Policy:                          "release-static-owner-private-0600",
			LifecycleOwner:                  "board orchestrator",
			RemoteTLSQualificationSupported: true,
		}, nil
	default:
		return Descriptor{}, ErrUnknownProfile
	}
}

// Plan is a validated, immutable config snapshot. Its zero value is unusable.
// It does not prove policy readiness, storage readiness, ownership or admission.
type Plan struct {
	config Config
	valid  bool
}

func (p Plan) Config() (Config, error) {
	if !p.valid {
		return Config{}, ErrConfig
	}
	return p.config, nil
}
func (p Plan) Descriptor() (Descriptor, error) {
	if !p.valid {
		return Descriptor{}, ErrConfig
	}
	return Describe(p.config.Profile)
}

// WriteStatus emits static mode capabilities, not a live readiness verdict.
func WriteStatus(profile string, w io.Writer) error {
	d, err := Describe(profile)
	if err != nil {
		return err
	}
	if w == nil {
		return ErrConfig
	}
	return json.NewEncoder(w).Encode(d)
}

func (p Plan) WriteStatus(w io.Writer) error {
	if !p.valid {
		return ErrConfig
	}
	return WriteStatus(p.config.Profile, w)
}

// NewClientTransport validates explicit client selection without requiring any
// server-private paths. It does not read projection sources or credentials.
func NewClientTransport(profile string, projection contract.Projection) (*client.UnixTransport, error) {
	if _, err := Describe(profile); err != nil {
		return nil, err
	}
	if err := client.ValidateProjection(projection); err != nil {
		return nil, ErrConfig
	}
	tr, err := client.NewUnixTransport(projection.Endpoint)
	if err != nil {
		return nil, ErrConfig
	}
	return tr, nil
}

// NewUnixTransport maps to the existing no-dial constructor. Projection remains
// separately validated; no credential source is required or consulted in UDS mode.
func (p Plan) NewUnixTransport() (*client.UnixTransport, error) {
	if !p.valid {
		return nil, ErrConfig
	}
	return client.NewUnixTransport(p.config.Endpoint)
}

// CheckProjection binds the client's selected endpoint to the validated plan.
// AccountID is only a preflight expectation, never proof of peer identity.
func (p Plan) CheckProjection(projection contract.Projection) error {
	if !p.valid || projection.Endpoint != p.config.Endpoint {
		return ErrConfig
	}
	return client.ValidateProjection(projection)
}

// WithStatusHeaders decorates the actual Unix handler, including /ready errors
// and /v1/version. It leaves status codes, body, auth, storage and policy checks
// untouched. Install it before Serve. Merely calling Describe does not wire a
// binary. The base handler must still check both storage and current authority.
func (p Plan) WithStatusHeaders(next http.Handler) (http.Handler, error) {
	d, err := p.Descriptor()
	if err != nil {
		return nil, err
	}
	if next == nil {
		return nil, ErrConfig
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(DescriptorHeader, string(b))
		next.ServeHTTP(w, r)
	}), nil
}
