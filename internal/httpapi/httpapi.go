// Package httpapi implements only the frozen swarma-board v1 HTTP boundary.
package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
)

const QueryLimit int64 = 8 << 20
const ArchiveLimit int64 = 64 << 20
const JSONLimit int64 = 64 << 20

const readBudget = 30 * time.Second
const writeBudget = 300 * time.Second
const wireAllowance = 5 * time.Second

// Both operation and wire deadlines start at request admission. A caller's
// earlier deadline always wins, including during body consumption and writes.
func cappedDeadline(caller context.Context, start time.Time, budget time.Duration) time.Time {
	deadline := start.Add(budget)
	if d, ok := caller.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	return deadline
}

func socketDeadlines(w http.ResponseWriter, caller context.Context, start time.Time, read, operation time.Duration) {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(cappedDeadline(caller, start, read))
	_ = controller.SetWriteDeadline(cappedDeadline(caller, start, operation+wireAllowance))
}

var boardID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var etagPattern = regexp.MustCompile(`^"rev-(0|[1-9][0-9]*)"$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var roles = map[string]string{
	"get": "viewer", "list": "viewer", "count": "viewer", "summary": "viewer", "plan": "viewer",
	"create": "editor", "set_details": "editor", "rename": "editor", "assign": "editor", "unassign": "editor", "set_status": "editor", "set_estimate": "editor", "link": "editor", "unlink": "editor", "check_item": "editor", "uncheck_item": "editor", "add_checklist_item": "editor", "remove_checklist_item": "editor", "set_notes": "editor", "set_parent": "editor", "delete": "editor",
}

// LogEvent contains only generated IDs, allowlisted route/operation and status.
// It intentionally contains no query, body, headers, URL or account data.
type LogEvent struct {
	RequestID, Route, Operation string
	Status                      int
	AffectedCount               int
}
type Options struct {
	Service    contract.Service
	Parser     contract.Parser
	Transfer   contract.Transfer
	Authorizer contract.Authorizer
	// PeerAuthenticator selects dedicated Unix-only auth. Authorizer still owns
	// current role checks; compose both with the same protected peer adapter.
	PeerAuthenticator PeerAuthenticator
	StorageReady      func(context.Context) error
	Commit            string
	Now               func() time.Time
	Log               func(LogEvent)
}
type Handler struct{ o Options }

func nilInterface(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Func, reflect.Slice, reflect.Interface:
		return r.IsNil()
	}
	return false
}
func New(o Options) (*Handler, error) {
	if nilInterface(o.Service) || nilInterface(o.Parser) || nilInterface(o.Transfer) || nilInterface(o.Authorizer) || o.StorageReady == nil {
		return nil, &contract.Error{Code: "unavailable", Message: "required server dependency missing"}
	}
	if o.PeerAuthenticator != nil && nilInterface(o.PeerAuthenticator) {
		return nil, problem("unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e := o.Authorizer.Ready(ctx); e != nil {
		return nil, &contract.Error{Code: "unavailable", Message: "authorization unavailable"}
	}
	if o.PeerAuthenticator != nil && o.PeerAuthenticator.Ready(ctx) != nil {
		return nil, problem("unavailable")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Commit == "" {
		o.Commit = "dev"
	}
	return &Handler{o: o}, nil
}

// Server configures transport bounds; integration owns listener creation/lifecycle.
func (h *Handler) Server(addr string) (*http.Server, error) {
	host, port, e := net.SplitHostPort(addr)
	if e != nil {
		return nil, problem("invalid_request")
	}
	ip := net.ParseIP(host)
	n, e := strconv.Atoi(port)
	if e != nil || n < 0 || n > 65535 || ip == nil || !ip.IsLoopback() {
		return nil, problem("invalid_request")
	}
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: writeBudget + wireAllowance, WriteTimeout: writeBudget + wireAllowance, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}, nil
}
func tag(r contract.Revision) string { return fmt.Sprintf(`"rev-%d"`, r) }
func problem(code string) error {
	return &contract.Error{Code: code, Message: strings.ReplaceAll(code, "_", " ")}
}

type response struct {
	http.ResponseWriter
	status int
}

func (w *response) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *response) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
		w.ResponseWriter.WriteHeader(n)
	}
}
func (w *response) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (h *Handler) ServeHTTP(writer http.ResponseWriter, r *http.Request) {
	start := time.Now()
	caller := r.Context()
	w := &response{ResponseWriter: writer}
	socketDeadlines(w, caller, start, readBudget, readBudget)
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	id := hex.EncodeToString(b)
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	event := LogEvent{RequestID: id, Route: "unknown"}
	defer func() {
		if x := recover(); x != nil {
			if w.status == 0 {
				writeError(w, id, problem("storage_error"))
			}
		}
		event.Status = w.status
		if h.o.Log != nil {
			h.o.Log(event)
		}
	}()
	path := r.URL.Path
	route, project, action, method := match(path)
	event.Route = route
	if route == "unknown" {
		writeError(w, id, problem("not_found"))
		return
	}
	timeout := readBudget
	if action == "export" || action == "import" {
		timeout = writeBudget
	}
	ctx, cancel := context.WithDeadline(caller, cappedDeadline(caller, start, timeout))
	defer cancel()
	r = r.WithContext(ctx)
	// Query bodies retain the ordinary read bound until the operation is parsed.
	// Import/export keep their admitted Update/Checkpoint budget through transfer.
	socketDeadlines(w, caller, start, timeout, timeout)
	if action == "import" || action == "export" {
		event.Operation = action
	}
	if h.o.PeerAuthenticator != nil && len(r.Header.Values("Authorization")) != 0 {
		writeError(w, id, problem("unauthorized"))
		return
	}
	public := route == "/health" || route == "/ready" || route == "/v1/version"
	var principal contract.Principal
	if !public {
		var e error
		principal, e = h.authenticate(r)
		if e != nil {
			writeError(w, id, e)
			return
		}
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeStatus(w, id, "invalid_request", 405)
		return
	}
	if r.URL.RawPath != "" && r.URL.RawPath != path {
		writeError(w, id, problem("invalid_request"))
		return
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		writeError(w, id, problem("invalid_request"))
		return
	}
	if project != "" && !boardID.MatchString(project) {
		writeError(w, id, problem("invalid_request"))
		return
	}
	if action == "" && len(q) > 0 {
		writeError(w, id, problem("invalid_request"))
		return
	}
	switch route {
	case "/health":
		writeJSON(w, 200, map[string]any{"status": "ok"})
		return
	case "/ready":
		if h.o.StorageReady(ctx) != nil || h.o.Authorizer.Ready(ctx) != nil || h.peerReady(ctx) != nil {
			writeError(w, id, problem("unavailable"))
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ready"})
		return
	case "/v1/version":
		writeJSON(w, 200, map[string]any{"product": "swarma-board", "commit": h.o.Commit, "contracts": map[string]string{"swarma_board_api": "v1", "task_plan": "v1", "snapshot_import": "v1", "revision_precondition": "v1"}})
		return
	case "/v1/me":
		writeJSON(w, 200, map[string]string{"account_id": principal.AccountID})
		return
	case "/v1/boards":
		p, e := h.o.Authorizer.Projects(ctx, principal)
		if e != nil {
			writeError(w, id, e)
			return
		}
		sort.Slice(p, func(i, j int) bool { return p[i].ID < p[j].ID })
		rows := []map[string]string{}
		for _, v := range p {
			rows = append(rows, map[string]string{"id": v.ID, "name": v.Name, "description": v.Description})
		}
		writeJSON(w, 200, map[string]any{"boards": rows})
		return
	}
	role := "viewer"
	if action == "import" {
		role = "admin"
	}
	if e = h.o.Authorizer.Require(ctx, principal, project, role); e != nil {
		writeError(w, id, e)
		return
	}
	switch action {
	case "q":
		h.query(w, r, id, project, principal, q, &event, caller, start)
	case "export":
		h.export(w, r, id, project, q, &event)
	case "import":
		h.importSnapshot(w, r, id, project, principal, q, &event)
	}
}
func match(path string) (route, project, action, method string) {
	switch path {
	case "/health", "/ready", "/v1/version", "/v1/me", "/v1/boards":
		return path, "", "", "GET"
	}
	parts := strings.Split(path, "/")
	if len(parts) == 5 && parts[1] == "v1" && parts[2] == "boards" && parts[3] != "" {
		switch parts[4] {
		case "q", "import":
			return "/v1/boards/{boardID}/" + parts[4], parts[3], parts[4], "POST"
		case "export":
			return "/v1/boards/{boardID}/export", parts[3], "export", "GET"
		}
	}
	return "unknown", "", "", ""
}
func (h *Handler) authenticate(r *http.Request) (contract.Principal, error) {
	for _, header := range []string{"X-Account-ID", "X-User-ID", "X-Remote-User", "X-Forwarded-User", "Remote-User", "X-Authenticated-User", "X-Peer-UID", "X-UID", "X-Forwarded-Uid"} {
		if len(r.Header.Values(header)) > 0 {
			return contract.Principal{}, problem("unauthorized")
		}
	}
	peer, captured := r.Context().Value(peerContextKey{}).(peerIdentity)
	if h.o.PeerAuthenticator != nil {
		if len(r.Header.Values("Authorization")) != 0 || !captured || !peer.ok {
			return contract.Principal{}, problem("unauthorized")
		}
		if h.peerReady(r.Context()) != nil {
			return contract.Principal{}, problem("unavailable")
		}
		p, e := h.o.PeerAuthenticator.AuthenticatePeer(r.Context(), peer.uid)
		if e != nil || p.AccountID == "" {
			return contract.Principal{}, problem("unauthorized")
		}
		return p, nil
	}
	// A Unix connection must never fall through to Bearer mode after an omitted
	// peer adapter or a failed credential extraction.
	if captured {
		return contract.Principal{}, problem("unauthorized")
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return contract.Principal{}, problem("unauthorized")
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n,") {
		return contract.Principal{}, problem("unauthorized")
	}
	p, e := h.o.Authorizer.Authenticate(r.Context(), token)
	if e != nil || p.AccountID == "" {
		return contract.Principal{}, problem("unauthorized")
	}
	return p, nil
}
func expected(r *http.Request) (contract.Revision, error) {
	v := r.Header.Values("If-Match")
	if len(v) == 0 {
		return 0, problem("revision_required")
	}
	if len(v) != 1 || !etagPattern.MatchString(v[0]) {
		return 0, problem("invalid_request")
	}
	s := v[0][5 : len(v[0])-1]
	n, e := strconv.ParseUint(s, 10, 64)
	if e != nil {
		return 0, problem("invalid_request")
	}
	return contract.Revision(n), nil
}
func media(r *http.Request, want string) bool {
	if len(r.Header.Values("Content-Type")) != 1 {
		return false
	}
	m, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || m != want {
		return false
	}
	for k, v := range params {
		if want != "text/plain" || k != "charset" || !strings.EqualFold(v, "utf-8") {
			return false
		}
	}
	return true
}
func body(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.ContentLength > limit {
		return nil, problem("payload_too_large")
	}
	// A context cancellation alone does not wake a blocked net/http body Read.
	// Join an already-running callback before handing the connection back.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(r.Context(), func() {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if e != nil {
		var over *http.MaxBytesError
		if errors.As(e, &over) {
			return nil, problem("payload_too_large")
		}
		var timeout net.Error
		if errors.As(e, &timeout) && timeout.Timeout() {
			return nil, problem("unavailable")
		}
		return nil, problem("invalid_request")
	}
	return b, nil
}
func audit(h *Handler, id string, p contract.Principal, op string) contract.AuditContext {
	return contract.AuditContext{AccountID: p.AccountID, RequestID: id, Operation: op, At: h.o.Now().UTC()}
}
func (h *Handler) query(w http.ResponseWriter, r *http.Request, id, project string, p contract.Principal, q url.Values, event *LogEvent, caller context.Context, start time.Time) {
	if len(q) != 1 || len(q["mode"]) != 1 || q.Get("mode") != "json" || !media(r, "text/plain") {
		writeError(w, id, problem("invalid_request"))
		return
	}
	b, e := body(w, r, QueryLimit)
	if e != nil {
		writeError(w, id, e)
		return
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		writeError(w, id, problem("invalid_request"))
		return
	}
	op, e := h.o.Parser.Parse(string(b))
	if e != nil {
		writeError(w, id, e)
		return
	}
	role, ok := roles[op.Name]
	if !ok {
		writeError(w, id, problem("unsupported_operation"))
		return
	}
	budget := readBudget
	if role == "editor" {
		budget = writeBudget
	}
	if err := r.Context().Err(); err != nil {
		writeError(w, id, err)
		return
	}
	// Parsing a mutation admits the longer operation budget, without restarting
	// its clock or extending the original caller's deadline.
	ctx, cancel := context.WithDeadline(caller, cappedDeadline(caller, start, budget))
	defer cancel()
	r = r.WithContext(ctx)
	socketDeadlines(w, caller, start, budget, budget)
	event.Operation = op.Name
	if e = h.o.Authorizer.Require(r.Context(), p, project, role); e != nil {
		writeError(w, id, e)
		return
	}
	call := contract.Call{ProjectID: project, Operation: op, Audit: audit(h, id, p, op.Name)}
	if role == "editor" {
		if len(r.Header.Values("Origin")) > 0 {
			writeError(w, id, problem("forbidden"))
			return
		}
		v, e := expected(r)
		if e != nil {
			writeError(w, id, e)
			return
		}
		call.Expected = &v
	}
	reply, e := h.o.Service.Execute(r.Context(), call)
	if e != nil {
		writeError(w, id, e)
		return
	}
	// Trusted adapters must still never accidentally return an in-band failure.
	var obj map[string]json.RawMessage
	if len(reply.JSON) > int(JSONLimit) || !utf8.Valid(reply.JSON) || json.Unmarshal(reply.JSON, &obj) != nil || len(obj) != 1 || len(obj[op.Name]) == 0 || inBandError(obj[op.Name]) {
		writeError(w, id, problem("storage_error"))
		return
	}
	if role == "editor" {
		var result struct {
			ChangedIDs []string `json:"changed_ids"`
		}
		if json.Unmarshal(obj[op.Name], &result) == nil {
			event.AffectedCount = len(result.ChangedIDs)
		}
	}
	w.Header().Set("ETag", tag(reply.Revision))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(reply.JSON)
}
func inBandError(b json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(b, &obj) != nil {
		return false
	}
	if _, ok := obj["error"]; ok {
		return true
	}
	if raw, ok := obj["success"]; ok && string(raw) == "false" {
		return true
	}
	return false
}
func (h *Handler) export(w http.ResponseWriter, r *http.Request, id, project string, q url.Values, event *LogEvent) {
	if len(q) > 0 {
		writeError(w, id, problem("invalid_request"))
		return
	}
	s, e := h.o.Service.Snapshot(r.Context(), project)
	if e != nil {
		writeError(w, id, e)
		return
	}
	b := &boundedBuffer{limit: ArchiveLimit}
	if e = h.o.Transfer.Encode(r.Context(), b, s); e != nil {
		writeError(w, id, e)
		return
	}
	event.AffectedCount = len(s.Elements)
	sum := sha256.Sum256(b.Bytes())
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("ETag", tag(s.Revision))
	w.Header().Set("X-Snapshot-SHA256", hex.EncodeToString(sum[:]))
	w.WriteHeader(200)
	_, _ = w.Write(b.Bytes())
}

type boundedBuffer struct {
	buf   bytes.Buffer
	limit int64
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }
func (b *boundedBuffer) Len() int      { return b.buf.Len() }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.limit {
		return 0, problem("payload_too_large")
	}
	return b.buf.Write(p)
}
func (h *Handler) importSnapshot(w http.ResponseWriter, r *http.Request, id, project string, p contract.Principal, q url.Values, event *LogEvent) {
	if len(q) > 1 || len(q) == 1 && (len(q["dry_run"]) != 1 || (q.Get("dry_run") != "true" && q.Get("dry_run") != "false")) || !media(r, "application/gzip") {
		writeError(w, id, problem("invalid_request"))
		return
	}
	if len(r.Header.Values("Origin")) > 0 {
		writeError(w, id, problem("forbidden"))
		return
	}
	dry := q.Get("dry_run") == "true"
	var revision contract.Revision
	var e error
	digest := ""
	if !dry {
		revision, e = expected(r)
		if e != nil {
			writeError(w, id, e)
			return
		}
		d := r.Header.Values("X-Snapshot-SHA256")
		if len(d) != 1 || !digestPattern.MatchString(d[0]) {
			writeError(w, id, problem("invalid_snapshot"))
			return
		}
		digest = d[0]
	}
	b, e := body(w, r, ArchiveLimit)
	if e != nil {
		writeError(w, id, e)
		return
	}
	sum := sha256.Sum256(b)
	actual := hex.EncodeToString(sum[:])
	if !dry && actual != digest {
		writeError(w, id, problem("invalid_snapshot"))
		return
	}
	prepared, e := h.o.Transfer.Prepare(r.Context(), bytes.NewReader(b), contract.ImportOptions{ProjectID: project, ExpectedSHA256: digest})
	if e != nil {
		writeError(w, id, e)
		return
	}
	if prepared.SHA256 != actual || prepared.Report.SHA256 != actual || prepared.Snapshot.Project.ID != project {
		writeError(w, id, problem("invalid_snapshot"))
		return
	}
	if dry {
		s, e := h.o.Service.Snapshot(r.Context(), project)
		if e != nil {
			writeError(w, id, e)
			return
		}
		w.Header().Set("ETag", tag(s.Revision))
		report := prepared.Report
		event.AffectedCount = report.ElementCount
		exclusions, warnings := report.Exclusions, report.Warnings
		if exclusions == nil {
			exclusions = []string{}
		}
		if warnings == nil {
			warnings = []string{}
		}
		writeJSON(w, 200, map[string]any{"import": map[string]any{"sha256": actual, "format": report.Format, "version": report.Version, "element_count": report.ElementCount, "exclusions": exclusions, "warnings": warnings}})
		return
	}
	if len(prepared.Report.Exclusions) > 0 {
		v := r.Header.Values("X-Import-Accept-Exclusions")
		if len(v) != 1 || v[0] != "true" {
			writeError(w, id, problem("invalid_snapshot"))
			return
		}
	}
	commit, e := h.o.Service.Import(r.Context(), project, revision, audit(h, id, p, "import"), prepared)
	if e != nil {
		writeError(w, id, e)
		return
	}
	event.AffectedCount = prepared.Report.ElementCount
	w.Header().Set("ETag", tag(commit.Revision))
	writeJSON(w, 200, map[string]any{"import": map[string]any{"revision": commit.Revision, "element_count": prepared.Report.ElementCount, "sha256": actual}})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, e := json.Marshal(v)
	if e != nil || int64(len(b)) > JSONLimit {
		status = 500
		b, _ = json.Marshal(map[string]any{"error": map[string]any{"code": "storage_error", "message": "storage error", "details": map[string]any{}}, "request_id": w.Header().Get("X-Request-ID")})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

var statuses = map[string]int{"invalid_request": 400, "unauthorized": 401, "forbidden": 403, "not_found": 404, "already_exists": 409, "dependency_cycle": 409, "dependency_conflict": 409, "revision_conflict": 412, "revision_required": 428, "payload_too_large": 413, "query_error": 422, "unsupported_operation": 422, "unsupported_batch": 422, "invalid_parent": 422, "blocked": 422, "estimate_required": 422, "not_assigned": 422, "invalid_snapshot": 422, "snapshot_version_unsupported": 422, "storage_error": 500, "unavailable": 503}

func writeStatus(w http.ResponseWriter, id, code string, status int) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": strings.ReplaceAll(code, "_", " "), "details": map[string]any{}}, "request_id": id})
}
func writeError(w http.ResponseWriter, id string, e error) {
	code := "storage_error"
	details := map[string]any{}
	var ce *contract.Error
	if errors.As(e, &ce) && ce != nil {
		if _, ok := statuses[ce.Code]; ok {
			code = ce.Code
		}
		details = safeDetails(ce.Details)
		if code == "revision_conflict" {
			switch v := ce.Details["current_revision"].(type) {
			case contract.Revision:
				details["current_revision"] = v
			case uint64:
				details["current_revision"] = v
			}
		}
	}
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		code = "unavailable"
	}
	writeJSON(w, statuses[code], map[string]any{"error": map[string]any{"code": code, "message": strings.ReplaceAll(code, "_", " "), "details": details}, "request_id": id})
}
