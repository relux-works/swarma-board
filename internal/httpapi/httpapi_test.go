package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/auth"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

type harness struct {
	h                                      *Handler
	revision                               contract.Revision
	executes, imports, snapshots, prepares int
	ready                                  bool
	logs                                   []LogEvent
	calls                                  []contract.Call
	exclusions                             []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	x := &harness{ready: true}
	data, e := os.ReadFile("../testadapter/testdata/server.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture map[string]any
	if e = json.Unmarshal(data, &fixture); e != nil {
		t.Fatal(e)
	}
	for i, raw := range fixture["tokens"].([]any) {
		sum := sha256.Sum256([]byte([]string{"http-viewer", "http-editor", "http-admin"}[i]))
		raw.(map[string]any)["sha256"] = hex.EncodeToString(sum[:])
	}
	data, e = json.Marshal(fixture)
	if e != nil {
		t.Fatal(e)
	}
	a, e := auth.New(bytes.NewReader(data), time.Now)
	if e != nil {
		t.Fatal(e)
	}
	o := Options{Authorizer: a, Commit: "synthetic-build", StorageReady: func(context.Context) error {
		if !x.ready {
			return errors.New("private path")
		}
		return nil
	}, Log: func(event LogEvent) { x.logs = append(x.logs, event) }, Parser: testadapter.ParserFunc(func(s string) (contract.Operation, error) {
		switch s {
		case "count()":
			return contract.Operation{Name: "count"}, nil
		case "set_notes(TASK-a,text='sensitive body')":
			return contract.Operation{Name: "set_notes"}, nil
		case "spawn()":
			return contract.Operation{Name: "spawn"}, nil
		case "count();count()":
			return contract.Operation{}, problem("unsupported_batch")
		default:
			return contract.Operation{}, &contract.Error{Code: "query_error", Message: s, Details: map[string]any{"body": s}}
		}
	})}
	o.Service = testadapter.Service{ExecuteFunc: func(ctx context.Context, c contract.Call) (contract.Reply, error) {
		x.executes++
		x.calls = append(x.calls, c)
		if c.Expected != nil {
			if *c.Expected != x.revision {
				return contract.Reply{}, &contract.Error{Code: "revision_conflict", Details: map[string]any{"current_revision": x.revision, "private": "sensitive body"}}
			}
			x.revision++
		}
		payload := fmt.Sprintf(`{"%s":{"id":"TASK-a","changed_ids":["TASK-a"]}}`, c.Operation.Name)
		if c.Operation.Name == "count" {
			payload = `{"count":0}`
		}
		return contract.Reply{Revision: x.revision, JSON: json.RawMessage(payload)}, nil
	}, SnapshotFunc: func(ctx context.Context, p string) (contract.Snapshot, error) {
		x.snapshots++
		return contract.Snapshot{Project: contract.Project{ID: p}, Revision: x.revision}, nil
	}, ImportFunc: func(ctx context.Context, p string, r contract.Revision, a contract.AuditContext, in contract.PreparedImport) (contract.Commit, error) {
		x.imports++
		if r != x.revision {
			return contract.Commit{}, &contract.Error{Code: "revision_conflict", Details: map[string]any{"current_revision": x.revision}}
		}
		x.revision++
		return contract.Commit{Revision: x.revision}, nil
	}}
	o.Transfer = testadapter.Transfer{PrepareFunc: func(ctx context.Context, r io.Reader, opts contract.ImportOptions) (contract.PreparedImport, error) {
		x.prepares++
		b, e := io.ReadAll(r)
		if e != nil {
			return contract.PreparedImport{}, e
		}
		sum := sha256.Sum256(b)
		d := hex.EncodeToString(sum[:])
		return contract.PreparedImport{SHA256: d, Snapshot: contract.Snapshot{Project: contract.Project{ID: opts.ProjectID}}, Report: contract.ImportReport{SHA256: d, Format: "swarma-board", Version: 1, ElementCount: 2, Exclusions: x.exclusions}}, nil
	}, EncodeFunc: func(ctx context.Context, w io.Writer, s contract.Snapshot) error {
		_, e := fmt.Fprintf(w, "synthetic archive at %s revision %d", s.Project.ID, s.Revision)
		return e
	}}
	x.h, e = New(o)
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func (x *harness) request(method, path, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	return w
}

const queryPath = "/v1/boards/synthetic-board/q?mode=json"
const importPath = "/v1/boards/synthetic-board/import"
const mutation = "set_notes(TASK-a,text='sensitive body')"

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
	if code != "" {
		var obj struct {
			Error     struct{ Code string }
			RequestID string `json:"request_id"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &obj); e != nil || obj.Error.Code != code || len(obj.RequestID) != 32 || obj.RequestID != w.Header().Get("X-Request-ID") {
			t.Fatalf("bad error envelope %s %v", w.Body.String(), e)
		}
	}
}
func TestRoutesMetadataAndReadiness(t *testing.T) {
	x := newHarness(t)
	for _, path := range []string{"/health", "/ready", "/v1/version"} {
		w := x.request("GET", path, "", "", nil)
		requireStatus(t, w, 200, "")
		if w.Header().Get("X-Request-ID") == "" {
			t.Fatal("no request ID")
		}
	}
	version := x.request("GET", "/v1/version", "", "", nil)
	if !strings.Contains(version.Body.String(), `"revision_precondition":"v1"`) || !strings.Contains(version.Body.String(), `"commit":"synthetic-build"`) {
		t.Fatal(version.Body.String())
	}
	x.ready = false
	requireStatus(t, x.request("GET", "/ready", "", "", nil), 503, "unavailable")
	for _, path := range []string{"/v1/me", "/v1/boards"} {
		requireStatus(t, x.request("GET", path, "", "", nil), 401, "unauthorized")
		requireStatus(t, x.request("GET", path, "http-viewer", "", nil), 200, "")
	}
	boards := x.request("GET", "/v1/boards", "http-viewer", "", nil)
	if !strings.Contains(boards.Body.String(), `"id":"synthetic-board"`) || strings.Contains(boards.Body.String(), `"ID"`) {
		t.Fatal(boards.Body.String())
	}
	requireStatus(t, x.request("GET", "/unknown", "", "", nil), 404, "not_found")
	w := x.request("PUT", "/health", "", "", nil)
	requireStatus(t, w, 405, "invalid_request")
	if w.Header().Get("Allow") != "GET" {
		t.Fatal("no Allow")
	}
	requireStatus(t, x.request("GET", "/health?account_id=forged", "", "", nil), 400, "invalid_request")
}
func TestRevisionPreconditionsCASAndReadOnly(t *testing.T) {
	x := newHarness(t)
	read := x.request("POST", queryPath, "http-viewer", "count()", map[string]string{"Content-Type": "text/plain; charset=utf-8"})
	requireStatus(t, read, 200, "")
	if read.Header().Get("ETag") != `"rev-0"` || x.revision != 0 {
		t.Fatal("read revision")
	}
	tests := []struct {
		tag    string
		status int
		code   string
	}{{"", 428, "revision_required"}, {"*", 400, "invalid_request"}, {`W/"rev-0"`, 400, "invalid_request"}, {`"rev-0", "rev-1"`, 400, "invalid_request"}, {`"rev-00"`, 400, "invalid_request"}, {`"rev-18446744073709551616"`, 400, "invalid_request"}, {`"rev-0"`, 200, ""}, {`"rev-0"`, 412, "revision_conflict"}}
	for _, tc := range tests {
		headers := map[string]string{"Content-Type": "text/plain"}
		if tc.tag != "" {
			headers["If-Match"] = tc.tag
		}
		w := x.request("POST", queryPath, "http-editor", mutation, headers)
		requireStatus(t, w, tc.status, tc.code)
		if tc.status == 412 {
			if !strings.Contains(w.Body.String(), `"current_revision":1`) || strings.Contains(w.Body.String(), "sensitive") {
				t.Fatal(w.Body.String())
			}
		}
	}
	if x.revision != 1 || x.executes != 3 {
		t.Fatal(x.revision, x.executes)
	}
	call := x.calls[1]
	if call.Audit.AccountID != "synthetic-editor" || call.Audit.Operation != "set_notes" || call.Audit.RequestID == "" || call.Audit.At.IsZero() {
		t.Fatal(call.Audit)
	}
	// Client-provided IDs never become server IDs, including in safe logs.
	w := x.request("POST", queryPath, "http-editor", "count()", map[string]string{"Content-Type": "text/plain", "X-Request-ID": "sensitive body"})
	requireStatus(t, w, 200, "")
	if w.Header().Get("X-Request-ID") == "sensitive body" {
		t.Fatal("trusted request id")
	}
	for _, log := range x.logs {
		b, _ := json.Marshal(log)
		if strings.Contains(string(b), "sensitive") || strings.Contains(string(b), "http-editor") {
			t.Fatal("unsafe log", string(b))
		}
	}
}
func TestAuthenticationAuthorizationAndTransportRefusals(t *testing.T) {
	cases := []struct {
		name, path, token, body string
		headers                 map[string]string
		status                  int
		code                    string
	}{
		{"no bearer", queryPath, "", "count()", nil, 401, "unauthorized"},
		{"bad bearer", queryPath, "bad", "count()", nil, 401, "unauthorized"},
		{"cookie authentication", queryPath, "", "count()", map[string]string{"Cookie": "token=http-admin"}, 401, "unauthorized"},
		{"proxy principal", queryPath, "http-admin", "count()", map[string]string{"X-Account-ID": "synthetic-admin"}, 401, "unauthorized"},
		{"viewer mutation", queryPath, "http-viewer", mutation, map[string]string{"Content-Type": "text/plain", "If-Match": `"rev-0"`}, 403, "forbidden"},
		{"foreign project", "/v1/boards/foreign/q?mode=json", "http-admin", "malformed secret query", map[string]string{"Content-Type": "text/plain"}, 403, "forbidden"},
		{"origin mutation", queryPath, "http-editor", mutation, map[string]string{"Content-Type": "text/plain", "If-Match": `"rev-0"`, "Origin": "https://example.invalid"}, 403, "forbidden"},
		{"missing media", queryPath, "http-editor", "count()", nil, 400, "invalid_request"},
		{"bad charset", queryPath, "http-editor", "count()", map[string]string{"Content-Type": "text/plain;charset=latin1"}, 400, "invalid_request"},
		{"invalid utf8", queryPath, "http-editor", string([]byte{0xff}), map[string]string{"Content-Type": "text/plain"}, 400, "invalid_request"},
		{"bad mode", "/v1/boards/synthetic-board/q?mode=xml", "http-viewer", "count()", map[string]string{"Content-Type": "text/plain"}, 400, "invalid_request"},
		{"duplicate mode", queryPath + "&mode=json", "http-viewer", "count()", map[string]string{"Content-Type": "text/plain"}, 400, "invalid_request"},
		{"unknown query", queryPath + "&account_id=forged", "http-viewer", "count()", map[string]string{"Content-Type": "text/plain"}, 400, "invalid_request"},
		{"invalid project", "/v1/boards/Bad/q?mode=json", "http-viewer", "count()", map[string]string{"Content-Type": "text/plain"}, 400, "invalid_request"},
		{"encoded project", "/v1/boards/%73ynthetic-board/q?mode=json", "http-viewer", "count()", map[string]string{"Content-Type": "text/plain"}, 400, "invalid_request"},
		{"removed operation", queryPath, "http-admin", "spawn()", map[string]string{"Content-Type": "text/plain"}, 422, "unsupported_operation"},
		{"batch", queryPath, "http-admin", "count();count()", map[string]string{"Content-Type": "text/plain"}, 422, "unsupported_batch"},
		{"safe parser error", queryPath, "http-admin", "sensitive body", map[string]string{"Content-Type": "text/plain"}, 422, "query_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newHarness(t)
			w := x.request("POST", tc.path, tc.token, tc.body, tc.headers)
			requireStatus(t, w, tc.status, tc.code)
			if x.executes != 0 || x.imports != 0 || x.snapshots != 0 {
				t.Fatal("called service before refusal")
			}
			if strings.Contains(w.Body.String(), "sensitive") {
				t.Fatal("echoed query")
			}
		})
	}
	x := newHarness(t)
	r := httptest.NewRequest("POST", queryPath, strings.NewReader(mutation))
	r.Header.Add("Authorization", "Bearer http-admin")
	r.Header.Add("Authorization", "Bearer http-admin")
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	requireStatus(t, w, 401, "unauthorized")
	r = httptest.NewRequest("POST", queryPath, strings.NewReader(mutation))
	r.Header.Set("Authorization", "Bearer http-admin")
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Add("If-Match", `"rev-0"`)
	r.Header.Add("If-Match", `"rev-0"`)
	w = httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	requireStatus(t, w, 400, "invalid_request")
}
func TestImportDryRunDigestConsentAndExportSnapshot(t *testing.T) {
	x := newHarness(t)
	archive := "synthetic bounded archive"
	sum := sha256.Sum256([]byte(archive))
	digest := hex.EncodeToString(sum[:])
	headers := map[string]string{"Content-Type": "application/gzip"}
	dry := x.request("POST", importPath+"?dry_run=true", "http-admin", archive, headers)
	requireStatus(t, dry, 200, "")
	if dry.Header().Get("ETag") != `"rev-0"` || x.imports != 0 || x.revision != 0 || !strings.Contains(dry.Body.String(), `"exclusions":[]`) {
		t.Fatal(dry.Body.String())
	}
	requireStatus(t, x.request("POST", importPath, "http-editor", archive, headers), 403, "forbidden")
	headers["If-Match"] = `"rev-0"`
	requireStatus(t, x.request("POST", importPath, "http-admin", archive, headers), 422, "invalid_snapshot")
	headers["X-Snapshot-SHA256"] = strings.Repeat("0", 64)
	before := x.prepares
	requireStatus(t, x.request("POST", importPath, "http-admin", archive, headers), 422, "invalid_snapshot")
	if x.prepares != before {
		t.Fatal("digest mismatch reached transfer")
	}
	headers["X-Snapshot-SHA256"] = digest
	x.exclusions = []string{"synthetic removed field"}
	requireStatus(t, x.request("POST", importPath, "http-admin", archive, headers), 422, "invalid_snapshot")
	if x.imports != 0 {
		t.Fatal("consent bypass")
	}
	headers["X-Import-Accept-Exclusions"] = "true"
	w := x.request("POST", importPath, "http-admin", archive, headers)
	requireStatus(t, w, 200, "")
	if w.Header().Get("ETag") != `"rev-1"` || x.imports != 1 || x.revision != 1 {
		t.Fatal(w.Body.String())
	}
	requireStatus(t, x.request("POST", importPath, "http-admin", archive, headers), 412, "revision_conflict")
	snapshots := x.snapshots
	w = x.request("GET", "/v1/boards/synthetic-board/export", "http-viewer", "", nil)
	requireStatus(t, w, 200, "")
	checksum := sha256.Sum256(w.Body.Bytes())
	if w.Header().Get("X-Snapshot-SHA256") != hex.EncodeToString(checksum[:]) || w.Header().Get("ETag") != `"rev-1"` || w.Header().Get("Content-Type") != "application/gzip" || x.snapshots != snapshots+1 || x.revision != 1 {
		t.Fatal(w.Header())
	}
	for _, query := range []string{"?dry_run=1", "?dry_run=true&dry_run=false", "?dry_run=true&unknown=yes"} {
		requireStatus(t, x.request("POST", importPath+query, "http-admin", archive, headers), 400, "invalid_request")
	}
	headers["Origin"] = "https://example.invalid"
	requireStatus(t, x.request("POST", importPath+"?dry_run=true", "http-admin", archive, headers), 403, "forbidden")
}
func TestBoundsErrorMappingAndInBandFailure(t *testing.T) {
	x := newHarness(t)
	w := x.request("POST", queryPath, "http-viewer", strings.Repeat("x", int(QueryLimit)+1), map[string]string{"Content-Type": "text/plain"})
	requireStatus(t, w, 413, "payload_too_large")
	// Chunked/unknown-length reads are bounded too.
	r := httptest.NewRequest("POST", queryPath, strings.NewReader(strings.Repeat("x", int(QueryLimit)+1)))
	r.ContentLength = -1
	r.Header.Set("Authorization", "Bearer http-viewer")
	r.Header.Set("Content-Type", "text/plain")
	out := httptest.NewRecorder()
	x.h.ServeHTTP(out, r)
	requireStatus(t, out, 413, "payload_too_large")
	r = httptest.NewRequest("POST", importPath+"?dry_run=true", strings.NewReader(""))
	r.ContentLength = ArchiveLimit + 1
	r.Header.Set("Authorization", "Bearer http-admin")
	r.Header.Set("Content-Type", "application/gzip")
	out = httptest.NewRecorder()
	x.h.ServeHTTP(out, r)
	requireStatus(t, out, 413, "payload_too_large")
	for code, status := range statuses {
		out := httptest.NewRecorder()
		writeError(out, "generated", &contract.Error{Code: code, Message: "private /path raw bearer", Details: map[string]any{"secret": "raw bearer"}})
		if out.Code != status || strings.Contains(out.Body.String(), "private") || strings.Contains(out.Body.String(), "bearer") {
			t.Fatal(code, out.Code, out.Body.String())
		}
	}
	for _, payload := range []string{`{"error":"private"}`, `{"set_notes":{"error":"private"}}`, `{"set_notes":{"success":false}}`, `invalid`} {
		x := newHarness(t)
		x.h.o.Service = testadapter.Service{ExecuteFunc: func(context.Context, contract.Call) (contract.Reply, error) {
			return contract.Reply{JSON: json.RawMessage(payload)}, nil
		}}
		requireStatus(t, x.request("POST", queryPath, "http-editor", mutation, map[string]string{"Content-Type": "text/plain", "If-Match": `"rev-0"`}), 500, "storage_error")
	}
	x.h.o.Transfer = testadapter.Transfer{EncodeFunc: func(ctx context.Context, w io.Writer, s contract.Snapshot) error {
		_, e := w.Write(make([]byte, ArchiveLimit+1))
		return e
	}}
	requireStatus(t, x.request("GET", "/v1/boards/synthetic-board/export", "http-viewer", "", nil), 413, "payload_too_large")
}
func TestStartupServerBoundsAndCommittedCancellation(t *testing.T) {
	if _, e := New(Options{}); e == nil {
		t.Fatal("missing authorizer accepted")
	}
	x := newHarness(t)
	o := x.h.o
	o.Authorizer = nil
	if _, e := New(o); e == nil {
		t.Fatal("missing authorizer accepted")
	}
	var nilAuth *auth.Authorizer
	o.Authorizer = nilAuth
	if _, e := New(o); e == nil {
		t.Fatal("typed nil authorizer accepted")
	}
	server, e := x.h.Server("127.0.0.1:0")
	if e != nil || server.ReadHeaderTimeout != 5*time.Second || server.MaxHeaderBytes != 32<<10 || server.ReadTimeout != 305*time.Second || server.WriteTimeout != 305*time.Second || server.IdleTimeout != 60*time.Second {
		t.Fatal(server, e)
	}
	for _, addr := range []string{"localhost:8080", "0.0.0.0:8080", "[::]:8080"} {
		if _, e = x.h.Server(addr); e == nil {
			t.Fatal("unsafe synthetic bind", addr)
		}
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	x.h.o.Service = testadapter.Service{ExecuteFunc: func(ctx context.Context, c contract.Call) (contract.Reply, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 300*time.Second {
			t.Fatal("missing query timeout")
		}
		cancel()
		return contract.Reply{Revision: 1, JSON: json.RawMessage(`{"set_notes":{"id":"TASK-a","changed_ids":["TASK-a"]}}`)}, nil
	}}
	r := httptest.NewRequest("POST", queryPath, strings.NewReader(mutation)).WithContext(base)
	r.Header.Set("Authorization", "Bearer http-editor")
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("If-Match", `"rev-0"`)
	w := httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	requireStatus(t, w, 200, "")
}

func TestExportBoundCannotBeBypassedByReaderFrom(t *testing.T) {
	b := &boundedBuffer{limit: 8}
	// LimitedReader has no WriterTo; io.Copy must not discover a promoted,
	// unbounded bytes.Buffer.ReadFrom on our bounded writer.
	_, e := io.Copy(b, io.LimitReader(strings.NewReader("123456789"), 9))
	if e == nil || b.Len() > 8 {
		t.Fatal("export bound bypassed", b.Len(), e)
	}
}

func TestSafeCycleDetailsSurviveWithoutReflectingPrivateData(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, "generated", &contract.Error{Code: "dependency_cycle", Message: "private query", Details: map[string]any{
		"cycle": []string{"TASK-a", "TASK-b", "TASK-a"}, "links": []map[string]string{{"blocked_id": "TASK-a", "blocker_id": "TASK-b", "secret": "private token"}}, "id": "TASK-a",
		"query": "private query", "path": "/private/storage.db", "project_id": "/private/storage.db", "members": []string{"private token"},
	}})
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"cycle":["TASK-a","TASK-b","TASK-a"]`) || !strings.Contains(w.Body.String(), `"blocked_id":"TASK-a"`) || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
}
