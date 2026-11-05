package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
)

const (
	MaxJSON    = 64 << 20
	MaxArchive = 64 << 20
	MaxQuery   = 8 << 20
)

// Options contains explicit composition inputs. UnixTransport must be created
// for the selected endpoint by NewUnixTransport; it is never selected implicitly.
// SyntheticHTTP permits literal-loopback HTTP only in an explicit test harness.
type Options struct {
	TLSConfig     *tls.Config
	UnixTransport *UnixTransport
	SyntheticHTTP bool
}
type Client struct {
	projection contract.Projection
	origin     string
	token      string
	http       *http.Client
	version    Version
}
type Version struct {
	Product   string            `json:"product"`
	Commit    string            `json:"commit"`
	Contracts map[string]string `json:"contracts"`
}
type Response struct {
	JSON      json.RawMessage
	Revision  contract.Revision
	RequestID string
}
type ImportOptions struct {
	DryRun           bool
	SHA256           string
	IfMatch          *contract.Revision
	AcceptExclusions bool
}

var projectID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var commitPattern = regexp.MustCompile(`^(dev|[0-9a-f]{40})$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateProjection accepts a Unix socket endpoint or a TCP origin. HTTP is
// structurally accepted here; New additionally requires explicit synthetic mode.
func ValidateProjection(p contract.Projection) error {
	u, err := url.Parse(p.Endpoint)
	if err != nil || u == nil {
		return LocalError("invalid_request", "invalid endpoint origin")
	}
	if u.Scheme == "unix" {
		if _, err := unixSocketPath(p.Endpoint); err != nil {
			return err
		}
	} else {
		if u.Opaque != "" || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.Contains(p.Endpoint, "#") {
			return LocalError("invalid_request", "invalid endpoint origin")
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
			return LocalError("invalid_request", "endpoint requires Unix socket, verified HTTPS or synthetic literal-loopback HTTP")
		}
		if strings.HasSuffix(u.Host, ":") {
			return LocalError("invalid_request", "invalid endpoint port")
		}
		if port := u.Port(); port != "" {
			n, e := strconv.Atoi(port)
			if e != nil || n < 1 || n > 65535 {
				return LocalError("invalid_request", "invalid endpoint port")
			}
		}
	}
	if !projectID.MatchString(p.ProjectID) || p.AccountID == "" || len(p.AccountID) > 256 || !utf8.ValidString(p.AccountID) || strings.ContainsFunc(p.AccountID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return LocalError("invalid_request", "projection requires exactly endpoint, project and account")
	}
	return nil
}

// New resolves one projection and completes version/account preflight. Unix
// mode never calls CredentialSource; TCP modes resolve exactly one credential.
// Close releases idle connections, including those in the injected transport.
func New(ctx context.Context, projections contract.ProjectionSource, credentials contract.CredentialSource, options Options) (*Client, error) {
	if projections == nil {
		return nil, LocalError("invalid_request", "projection and credential adapters are required")
	}
	p, err := projections.Resolve(ctx)
	if err != nil {
		return nil, LocalError("invalid_request", "projection resolution failed")
	}
	if err = ValidateProjection(p); err != nil {
		return nil, err
	}
	u, _ := url.Parse(p.Endpoint)
	var token string
	var tr http.RoundTripper
	origin := strings.TrimSuffix(p.Endpoint, "/")
	if u.Scheme == "unix" {
		if options.UnixTransport == nil || options.UnixTransport.transport == nil || options.UnixTransport.endpoint != p.Endpoint || options.TLSConfig != nil {
			return nil, LocalError("invalid_request", "matching Unix transport injection is required")
		}
		tr, origin = options.UnixTransport, unixOrigin
	} else {
		if options.UnixTransport != nil {
			return nil, LocalError("invalid_request", "Unix transport cannot serve a TCP endpoint")
		}
		if u.Scheme == "http" && !options.SyntheticHTTP {
			return nil, LocalError("invalid_request", "loopback HTTP requires explicit synthetic mode")
		}
		tcp := newHTTPTransport()
		if options.TLSConfig != nil {
			t := options.TLSConfig.Clone()
			if t.InsecureSkipVerify || (t.ServerName != "" && t.ServerName != u.Hostname()) {
				return nil, LocalError("invalid_request", "TLS certificate and endpoint hostname verification are required")
			}
			tcp.TLSClientConfig = t
		}
		if credentials == nil {
			return nil, LocalError("invalid_request", "projection and credential adapters are required")
		}
		token, err = credentials.Bearer(ctx, p)
		if err != nil || token == "" || len(token) > 4096 || !utf8.ValidString(token) || strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil, LocalError("invalid_request", "credential resolution failed")
		}
		tr = tcp
	}
	c := &Client{projection: p, origin: origin, token: token, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ok := false
	defer func() {
		if !ok {
			c.Close()
		}
	}()
	b, _, _, err := c.request(ctx, http.MethodGet, "/v1/version", nil, nil, false, false)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(b, &c.version) != nil || c.version.Product != "swarma-board" || !commitPattern.MatchString(c.version.Commit) {
		return nil, LocalError("invalid_request", "incompatible server version")
	}
	for _, name := range []string{"swarma_board_api", "task_plan", "snapshot_import", "revision_precondition"} {
		if c.version.Contracts[name] != "v1" {
			return nil, LocalError("invalid_request", "server lacks required v1 capabilities")
		}
	}
	b, _, _, err = c.request(ctx, http.MethodGet, "/v1/me", nil, nil, false, false)
	if err != nil {
		return nil, err
	}
	var me struct {
		AccountID string `json:"account_id"`
	}
	if json.Unmarshal(b, &me) != nil || me.AccountID != p.AccountID {
		return nil, LocalError("invalid_request", "server account does not match selected account")
	}
	safeContracts := map[string]string{}
	for _, name := range []string{"swarma_board_api", "task_plan", "snapshot_import", "revision_precondition"} {
		safeContracts[name] = "v1"
	}
	c.version.Contracts = safeContracts
	ok = true
	return c, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) ServerVersion() Version {
	v := c.version
	v.Contracts = map[string]string{}
	for k, s := range c.version.Contracts {
		v.Contracts[k] = s
	}
	return v
}
func (c *Client) path(suffix string) string { return "/v1/boards/" + c.projection.ProjectID + suffix }
func ETag(rev contract.Revision) string     { return `"rev-` + strconv.FormatUint(uint64(rev), 10) + `"` }
func ParseETag(s string) (contract.Revision, error) {
	if !strings.HasPrefix(s, `"rev-`) || !strings.HasSuffix(s, `"`) {
		return 0, LocalError("invalid_request", "server returned invalid revision ETag")
	}
	n := s[5 : len(s)-1]
	if n == "" || (len(n) > 1 && n[0] == '0') {
		return 0, LocalError("invalid_request", "server returned invalid revision ETag")
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return 0, LocalError("invalid_request", "server returned invalid revision ETag")
		}
	}
	v, e := strconv.ParseUint(n, 10, 64)
	if e != nil {
		return 0, LocalError("invalid_request", "server returned invalid revision ETag")
	}
	return contract.Revision(v), nil
}

// request never sets GetBody; even the standard transport cannot replay writes.
func (c *Client) request(ctx context.Context, method, path string, body []byte, headers http.Header, write, archive bool) ([]byte, http.Header, string, error) {
	timeout := 35 * time.Second
	if archive {
		timeout = 125 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = struct{ io.Reader }{bytes.NewReader(body)}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, reader)
	if err != nil {
		return nil, nil, "", transportError(write)
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if c.token == "" {
		if hasAuthorization(req.Header) {
			return nil, nil, "", LocalError("invalid_request", "authorization headers are forbidden on Unix transport")
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if archive && method == http.MethodGet {
		req.Header.Set("Accept", "application/gzip")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, nil, "", requestError(err, write)
	}
	defer res.Body.Close()
	id := res.Header.Get("X-Request-ID")
	if !safeID.MatchString(id) || (c.token != "" && strings.Contains(id, c.token)) {
		id = RequestID()
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, nil, id, failure("transport_error", "server redirect refused", id, res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxJSON+1))
	if err != nil {
		return nil, nil, id, transportError(write)
	}
	if len(b) > MaxJSON {
		message := "server response exceeds transport limit"
		if write {
			message = "write response exceeds transport limit; outcome unknown; read and reconcile before another write"
		}
		return nil, nil, id, failure("payload_too_large", message, id, 413)
	}
	// Detect in-band errors even on HTTP 200. Never relay arbitrary server text.
	var envelope struct {
		Error *struct {
			Code    string                     `json:"code"`
			Details map[string]json.RawMessage `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &envelope)
	if res.StatusCode < 200 || res.StatusCode >= 300 || envelope.Error != nil {
		code := statusCode(res.StatusCode)
		if envelope.Error != nil && knownCode(envelope.Error.Code) {
			code = envelope.Error.Code
		}
		e := failure(code, safeMessage(code), id, res.StatusCode)
		if envelope.Error != nil {
			var rev uint64
			if raw, ok := envelope.Error.Details["current_revision"]; ok && json.Unmarshal(raw, &rev) == nil {
				e.Details["current_revision"] = rev
			}
		}
		return nil, nil, id, e
	}
	if !(archive && method == http.MethodGet) && !unambiguousJSON(b) {
		return nil, nil, id, protocolError(write)
	}
	return b, res.Header, id, nil
}
func statusCode(status int) string {
	switch status {
	case 400:
		return "invalid_request"
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 409:
		return "dependency_conflict"
	case 412:
		return "revision_conflict"
	case 428:
		return "revision_required"
	case 413:
		return "payload_too_large"
	case 422:
		return "query_error"
	case 500:
		return "storage_error"
	case 503:
		return "unavailable"
	default:
		return "transport_error"
	}
}
func knownCode(code string) bool {
	switch code {
	case "invalid_request", "unauthorized", "forbidden", "not_found", "already_exists", "dependency_cycle", "dependency_conflict", "revision_conflict", "revision_required", "payload_too_large", "query_error", "unsupported_operation", "unsupported_batch", "invalid_parent", "blocked", "estimate_required", "not_assigned", "invalid_snapshot", "snapshot_version_unsupported", "storage_error", "unavailable":
		return true
	}
	return false
}

// Query sends a validated single statement. CLI owns grammar/allowlist validation.
func (c *Client) Query(ctx context.Context, query string, mutation bool, expected *contract.Revision) (Response, error) {
	if len(query) > MaxQuery {
		return Response{}, LocalError("payload_too_large", "query exceeds 8 MiB")
	}
	if !utf8.ValidString(query) {
		return Response{}, LocalError("query_error", "query must be UTF-8")
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "text/plain; charset=utf-8")
	if mutation {
		rev, err := c.expected(ctx, expected)
		if err != nil {
			return Response{}, err
		}
		headers.Set("If-Match", ETag(rev))
	}
	b, h, id, err := c.request(ctx, http.MethodPost, c.path("/q?mode=json"), []byte(query), headers, mutation, false)
	if err != nil {
		return Response{}, err
	}
	name := strings.TrimSpace(strings.SplitN(query, "(", 2)[0])
	var result map[string]json.RawMessage
	if json.Unmarshal(b, &result) != nil || len(result) != 1 || result[name] == nil || string(result[name]) == "null" {
		return Response{}, protocolError(mutation)
	}
	if mutation {
		var changed struct {
			ID         string   `json:"id"`
			ChangedIDs []string `json:"changed_ids"`
		}
		if json.Unmarshal(result[name], &changed) != nil || changed.ID == "" || changed.ChangedIDs == nil {
			return Response{}, transportError(true)
		}
	}
	rev, err := responseRevision(h)
	if err != nil {
		if mutation {
			return Response{}, transportError(true)
		}
		return Response{}, err
	}
	return Response{JSON: b, Revision: rev, RequestID: id}, nil
}
func (c *Client) expected(ctx context.Context, rev *contract.Revision) (contract.Revision, error) {
	if rev != nil {
		return *rev, nil
	}
	r, err := c.Query(ctx, "count()", false, nil)
	if err != nil {
		return 0, err
	}
	var count struct {
		Count *uint64 `json:"count"`
	}
	if json.Unmarshal(r.JSON, &count) != nil || count.Count == nil {
		return 0, LocalError("invalid_request", "invalid revision preflight response")
	}
	return r.Revision, nil
}
func (c *Client) Import(ctx context.Context, archive []byte, o ImportOptions) (Response, error) {
	if len(archive) > MaxArchive {
		return Response{}, LocalError("payload_too_large", "archive exceeds 64 MiB")
	}
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	if o.SHA256 != "" && (!digestPattern.MatchString(o.SHA256) || o.SHA256 != digest) {
		return Response{}, LocalError("invalid_snapshot", "archive digest does not match --sha256")
	}
	if !o.DryRun && o.SHA256 == "" {
		return Response{}, LocalError("invalid_snapshot", "import commit requires --sha256 from validation")
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/gzip")
	headers.Set("X-Snapshot-SHA256", digest)
	suffix := "/import?dry_run=true"
	if !o.DryRun {
		suffix = "/import?dry_run=false"
		rev, err := c.expected(ctx, o.IfMatch)
		if err != nil {
			return Response{}, err
		}
		headers.Set("If-Match", ETag(rev))
		if o.AcceptExclusions {
			headers.Set("X-Import-Accept-Exclusions", "true")
		}
	}
	b, h, id, err := c.request(ctx, http.MethodPost, c.path(suffix), archive, headers, !o.DryRun, true)
	if err != nil {
		return Response{}, err
	}
	var report struct {
		Import *struct {
			SHA256       string             `json:"sha256"`
			Revision     *contract.Revision `json:"revision"`
			ElementCount *int               `json:"element_count"`
			Exclusions   []string           `json:"exclusions"`
		} `json:"import"`
	}
	if json.Unmarshal(b, &report) != nil || report.Import == nil || report.Import.SHA256 != digest || report.Import.ElementCount == nil || *report.Import.ElementCount < 0 {
		return Response{}, protocolError(!o.DryRun)
	}
	if !o.DryRun && report.Import.Revision == nil {
		return Response{}, transportError(true)
	}
	rev, err := responseRevision(h)
	if err != nil {
		if !o.DryRun {
			return Response{}, transportError(true)
		}
		return Response{}, err
	}
	if !o.DryRun && *report.Import.Revision != rev {
		return Response{}, transportError(true)
	}
	return Response{JSON: b, Revision: rev, RequestID: id}, nil
}
func (c *Client) Export(ctx context.Context) ([]byte, contract.Revision, error) {
	b, h, _, err := c.request(ctx, http.MethodGet, c.path("/export"), nil, nil, false, true)
	if err != nil {
		return nil, 0, err
	}
	if strings.Split(h.Get("Content-Type"), ";")[0] != "application/gzip" {
		return nil, 0, LocalError("invalid_snapshot", "export is not a gzip archive")
	}
	sum := sha256.Sum256(b)
	if len(h.Values("X-Snapshot-SHA256")) != 1 || h.Get("X-Snapshot-SHA256") != hex.EncodeToString(sum[:]) {
		return nil, 0, LocalError("invalid_snapshot", "export digest mismatch")
	}
	rev, err := responseRevision(h)
	return b, rev, err
}

func responseRevision(h http.Header) (contract.Revision, error) {
	if len(h.Values("ETag")) != 1 {
		return 0, LocalError("invalid_request", "server returned ambiguous revision ETag")
	}
	return ParseETag(h.Get("ETag"))
}

// Reject ambiguous JSON before any capability/account decision or success.
func unambiguousJSON(b []byte) bool {
	if !utf8.Valid(b) || !json.Valid(b) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {

		tok, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return false
				}
				seen[s] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
