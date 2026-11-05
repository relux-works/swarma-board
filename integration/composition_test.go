// Package integration qualifies synthetic docking of the four concrete producers.
// It provides no Host UID, production credential or signed-export evidence.
package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/relux-works/swarma-board/internal/app"
	"github.com/relux-works/swarma-board/internal/cli"
	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/store"
	"github.com/relux-works/swarma-board/internal/testadapter"
	"github.com/relux-works/swarma-board/internal/transfer"
)

const project = "synthetic-board"
const build = "0123456789abcdef0123456789abcdef01234567"

type credential string

func (c credential) Bearer(context.Context, contract.Projection) (string, error) {
	return string(c), nil
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("..", "internal", "testadapter", "testdata", name))
	if e != nil {
		t.Fatal(e)
	}
	return b
}

type harness struct {
	app    *app.Synthetic
	server *httptest.Server
	tls    *tls.Config
	close  func()
}

func start(t *testing.T, dir string, secure bool) *harness {
	t.Helper()
	return startFixture(t, dir, secure, fixture(t, "server.json"))
}

func startFixture(t *testing.T, dir string, secure bool, data []byte) *harness {
	t.Helper()
	a, e := app.ConcreteSynthetic(context.Background(), app.SyntheticOptions{Fixture: bytes.NewReader(data), DataDir: dir, Listen: "127.0.0.1:0", Commit: build})
	if e != nil {
		t.Fatal(e)
	}
	s := httptest.NewUnstartedServer(a.Handler)
	var roots *tls.Config
	if secure {
		s.StartTLS()
		roots = s.Client().Transport.(*http.Transport).TLSClientConfig
	} else {
		s.Start()
	}
	var once sync.Once
	h := &harness{app: a, server: s, tls: roots}
	h.close = func() {
		once.Do(func() {
			s.Close()
			if e := a.Close(); e != nil {
				t.Error(e)
			}
		})
	}
	t.Cleanup(h.close)
	return h
}
func (h *harness) options(role string) cli.Options {
	return cli.Options{Projection: testadapter.Projection{Value: contract.Projection{Endpoint: h.server.URL, ProjectID: project, AccountID: "synthetic-" + role}}, Credential: credential("swarma-board-synthetic-" + role), TLSConfig: h.tls, Commit: build}
}
func (h *harness) client(t *testing.T, role string) *client.Client {
	t.Helper()
	o := h.options(role)
	c, e := client.New(context.Background(), o.Projection, o.Credential, client.Options{TLSConfig: o.TLSConfig})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	return c
}
func run(h *harness, args ...string) (int, []byte, []byte) {
	o := h.options("admin")
	var out, err bytes.Buffer
	o.Out = &out
	o.Err = &err
	code := cli.Run(context.Background(), args, o)
	return code, out.Bytes(), err.Bytes()
}
func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func imported(t *testing.T, c *client.Client, b []byte) {
	t.Helper()
	zero := contract.Revision(0)
	r, e := c.Import(context.Background(), b, client.ImportOptions{SHA256: digest(b), IfMatch: &zero})
	if e != nil || r.Revision != 1 {
		t.Fatalf("import revision %v: %v", r.Revision, e)
	}
}
func query(t *testing.T, c *client.Client, q string) client.Response {
	t.Helper()
	r, e := c.Query(context.Background(), q, false, nil)
	if e != nil {
		t.Fatalf("%s: %v", q, e)
	}
	return r
}
func code(t *testing.T, e error, want string) {
	t.Helper()
	var ce *client.Error
	if !errors.As(e, &ce) || ce.Code != want {
		t.Fatalf("wanted %s, got %v", want, e)
	}
}

func TestHTTPAndVerifiedTLSDockFactsPlansAndCLI(t *testing.T) {
	var baseline json.RawMessage
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "synthetic-loopback", true: "verified-TLS-test-adapter"}[secure], func(t *testing.T) {
			h := start(t, filepath.Join(t.TempDir(), "data"), secure)
			c := h.client(t, "admin")
			b := fixture(t, "native-v1.tar.gz")
			dry, e := c.Import(context.Background(), b, client.ImportOptions{DryRun: true})
			if e != nil || dry.Revision != 0 {
				t.Fatalf("dry-run: %v %v", dry.Revision, e)
			}
			before, e := h.app.Service.Snapshot(context.Background(), project)
			if e != nil || before.Revision != 0 || len(before.Elements) != 0 {
				t.Fatalf("dry-run wrote: %#v %v", before, e)
			}
			imported(t, c, b)
			r := query(t, c, "plan()")
			var got struct {
				Plan struct {
					Usable  bool `json:"usable"`
					Summary struct {
						TotalElements int `json:"totalElements"`
						TotalPhases   int `json:"totalPhases"`
					} `json:"summary"`
					CriticalPath []string `json:"criticalPath"`
					Phases       []struct {
						Phase    int `json:"phase"`
						Elements []struct {
							ID string `json:"id"`
						} `json:"elements"`
					} `json:"phases"`
				} `json:"plan"`
			}
			if e = json.Unmarshal(r.JSON, &got); e != nil {
				t.Fatal(e)
			}
			if !got.Plan.Usable || got.Plan.Summary.TotalElements != 4 || got.Plan.Summary.TotalPhases != 3 || !reflect.DeepEqual(got.Plan.CriticalPath, []string{"TASK-001", "TASK-003", "TASK-004"}) {
				t.Fatalf("wrong integrated plan: %s", r.JSON)
			}
			wantWaves := [][]string{{"TASK-001", "TASK-002"}, {"TASK-003"}, {"TASK-004"}}
			var waves [][]string
			for i, p := range got.Plan.Phases {
				if p.Phase != i+1 {
					t.Fatalf("number: %d", p.Phase)
				}
				ids := []string{}
				for _, el := range p.Elements {
					ids = append(ids, el.ID)
				}
				waves = append(waves, ids)
			}
			if !reflect.DeepEqual(waves, wantWaves) {
				t.Fatalf("waves %#v", waves)
			}
			if baseline == nil {
				baseline = append(json.RawMessage(nil), r.JSON...)
			} else if !bytes.Equal(baseline, r.JSON) {
				t.Fatal("TLS changed plan")
			}
			status, out, errout := run(h, "--json", "plan", "--scope", "TASK-001,TASK-002", "--mode", "children")
			if status != 0 || !bytes.Contains(out, []byte(`"plan"`)) {
				t.Fatalf("scope CLI %d %s %s", status, out, errout)
			}
			status, out, errout = run(h, "--json", "plan", "--critical-path")
			if status != 0 || !bytes.Contains(out, []byte(`"TASK-004"`)) {
				t.Fatalf("path CLI %d %s %s", status, out, errout)
			}
			source, e := h.app.Service.Snapshot(context.Background(), project)
			if e != nil {
				t.Fatal(e)
			}
			archive, rev, e := c.Export(context.Background())
			if e != nil || rev != 1 {
				t.Fatalf("export: %v", e)
			}
			prepared, e := transfer.New(build).Prepare(context.Background(), bytes.NewReader(archive), contract.ImportOptions{ProjectID: project, ExpectedSHA256: digest(archive)})
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(source.Elements, prepared.Snapshot.Elements) {
				t.Fatal("roundtrip changed native facts")
			}
			second := start(t, filepath.Join(t.TempDir(), "roundtrip"), secure)
			imported(t, second.client(t, "admin"), archive)
			copied, e := second.app.Service.Snapshot(context.Background(), project)
			if e != nil || !reflect.DeepEqual(source.Elements, copied.Elements) {
				t.Fatalf("second import facts: %v", e)
			}
			if !bytes.Equal(query(t, second.client(t, "admin"), "plan()").JSON, r.JSON) {
				t.Fatal("roundtrip plan changed")
			}
		})
	}
}

func TestCASGrantsRefusedImportAndRestartAudit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	h := start(t, dir, false)
	admin := h.client(t, "admin")
	imported(t, admin, fixture(t, "native-v1.tar.gz"))
	editor := h.client(t, "editor")
	viewer := h.client(t, "viewer")
	rev := contract.Revision(1)
	_, e := viewer.Query(context.Background(), `set_notes(TASK-001,text="forbidden")`, true, &rev)
	code(t, e, "forbidden")
	_, e = admin.Import(context.Background(), fixture(t, "native-v1.tar.gz"), client.ImportOptions{SHA256: digest(fixture(t, "native-v1.tar.gz")), IfMatch: &rev})
	code(t, e, "already_exists")
	before, e := h.app.Service.Snapshot(context.Background(), project)
	if e != nil {
		t.Fatal(e)
	}
	_, e = editor.Query(context.Background(), `link(TASK-001,blocked_by=TASK-004)`, true, &rev)
	code(t, e, "dependency_cycle")
	after, e := h.app.Service.Snapshot(context.Background(), project)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refusal changed state: %v", e)
	}
	startGate := make(chan struct{})
	results := make(chan error, 2)
	for _, text := range []string{"one", "two"} {
		go func(text string) {
			<-startGate
			_, e := editor.Query(context.Background(), `set_notes(TASK-001,text="`+text+`",set=true)`, true, &rev)
			results <- e
		}(text)
	}
	close(startGate)
	wins, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			wins++
		} else {
			code(t, e, "revision_conflict")
			conflicts++
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("CAS wins=%d conflicts=%d", wins, conflicts)
	}
	current, e := h.app.Service.Snapshot(context.Background(), project)
	if e != nil || current.Revision != 2 {
		t.Fatalf("revision: %v %v", current.Revision, e)
	}
	var backup bytes.Buffer
	if e = h.app.Registry.Backup(context.Background(), project, &backup); e != nil {
		t.Fatal(e)
	}
	h.close()
	db, e := store.Open(filepath.Join(dir, project+".db"), contract.Project{ID: project}, false)
	if e != nil {
		t.Fatal(e)
	}
	audit, e := db.Audit(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(audit) != 2 || audit[0].Revision != 1 || audit[0].Context.AccountID != "synthetic-admin" || audit[0].Context.Operation != "import" || audit[1].Revision != 2 || audit[1].Context.AccountID != "synthetic-editor" || audit[1].Context.Operation != "set_notes" || audit[1].Context.RequestID == "" {
		t.Fatalf("audit %#v", audit)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	restarted := start(t, dir, false)
	persisted, e := restarted.app.Service.Snapshot(context.Background(), project)
	if e != nil || !reflect.DeepEqual(current, persisted) {
		t.Fatalf("restart changed state: %v", e)
	}
	restoreDir := filepath.Join(t.TempDir(), "restored")
	if e = os.Mkdir(restoreDir, 0700); e != nil {
		t.Fatal(e)
	}
	if e = store.Restore(context.Background(), filepath.Join(restoreDir, project+".db"), contract.Project{ID: project}, bytes.NewReader(backup.Bytes())); e != nil {
		t.Fatal(e)
	}
	restored := start(t, restoreDir, false)
	snap, e := restored.app.Service.Snapshot(context.Background(), project)
	if e != nil || !reflect.DeepEqual(current, snap) {
		t.Fatalf("restore state: %v", e)
	}
}

func TestDeadServiceAndMissingProductionAdaptersRefuse(t *testing.T) {
	var out bytes.Buffer
	if n := app.RunSyntheticServer(context.Background(), nil, build, &out, app.ConcreteSynthetic); n == 0 || !bytes.Contains(out.Bytes(), []byte("production composition is gated")) {
		t.Fatalf("startup %d %s", n, &out)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	endpoint := "http://" + ln.Addr().String()
	ln.Close()
	var stdout, stderr bytes.Buffer
	n := cli.Run(context.Background(), []string{"q", "count()"}, cli.Options{Projection: testadapter.Projection{Value: contract.Projection{Endpoint: endpoint, ProjectID: project, AccountID: "synthetic-admin"}}, Credential: credential("swarma-board-synthetic-admin"), Out: &stdout, Err: &stderr})
	if n != 1 || stdout.Len() != 0 || !bytes.Contains(stderr.Bytes(), []byte("transport_error")) {
		t.Fatalf("dead service: %d %s %s", n, &stdout, &stderr)
	}
	stdout.Reset()
	stderr.Reset()
	n = cli.Run(context.Background(), []string{"q", "count()"}, cli.Options{Out: &stdout, Err: &stderr})
	if n != 2 || stdout.Len() != 0 {
		t.Fatalf("missing adapters: %d %s %s", n, &stdout, &stderr)
	}
}

func TestTwoProjectCLITaskFactsRemainIsolatedOnRestart(t *testing.T) {
	var config map[string]json.RawMessage
	if e := json.Unmarshal(fixture(t, "server.json"), &config); e != nil {
		t.Fatal(e)
	}
	appendRow := func(key string, row any) {
		var rows []json.RawMessage
		if e := json.Unmarshal(config[key], &rows); e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		rows = append(rows, b)
		config[key], e = json.Marshal(rows)
		if e != nil {
			t.Fatal(e)
		}
	}
	appendRow("projects", map[string]any{"id": "synthetic-other", "name": "Other", "description": "Synthetic only"})
	appendRow("accounts", map[string]any{"id": "synthetic-other-admin", "disabled": false})
	appendRow("tokens", map[string]any{"sha256": digest([]byte("swarma-board-synthetic-other-admin")), "account_id": "synthetic-other-admin", "revoked": false, "expires_at": nil})
	appendRow("grants", map[string]any{"account_id": "synthetic-other-admin", "project_id": "synthetic-other", "role": "admin"})
	data, e := json.Marshal(config)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "data")
	h := startFixture(t, dir, false, data)
	mutate := func(q string) string {
		t.Helper()
		n, out, errout := run(h, "m", q)
		if n != 0 {
			t.Fatalf("mutation %s: %d %s %s", q, n, out, errout)
		}
		var object map[string]struct {
			ID string `json:"id"`
		}
		if e := json.Unmarshal(out, &object); e != nil {
			t.Fatal(e)
		}
		for _, row := range object {
			return row.ID
		}
		t.Fatal("missing mutation result")
		return ""
	}
	epic := mutate(`create(type=epic,name="CLI epic")`)
	story := mutate(`create(type=story,name="CLI story",parent=` + epic + `)`)
	task := mutate(`create(type=task,name="CLI task",parent=` + story + `)`)
	if !regexp.MustCompile(`^TASK-[0-9]{6}-[a-z0-9]{6}$`).MatchString(task) {
		t.Fatalf("noncanonical created ID %s", task)
	}
	mutate(`assign(` + task + `,agent="synthetic-label")`)
	mutate(`set_estimate(` + task + `,estimate=estimated,scale=fibonacci,number=3)`)
	mutate(`add_checklist_item(` + task + `,text="retained checklist")`)
	mutate(`check_item(` + task + `,item=1)`)
	mutate(`set_notes(` + task + `,text="retained notes",set=true)`)
	mutate(`set_status(` + task + `,status=development)`)
	disposable := mutate(`create(type=task,name="disposable",parent=` + story + `)`)
	mutate(`delete(` + disposable + `,confirm=true)`)
	a, e := h.app.Service.Snapshot(context.Background(), project)
	if e != nil {
		t.Fatal(e)
	}
	b, e := h.app.Service.Snapshot(context.Background(), "synthetic-other")
	if e != nil || b.Revision != 0 || len(b.Elements) != 0 {
		t.Fatalf("foreign project changed %v %#v", e, b)
	}
	admin := h.client(t, "admin")
	o := h.options("admin")
	o.Projection = testadapter.Projection{Value: contract.Projection{Endpoint: h.server.URL, ProjectID: "synthetic-other", AccountID: "synthetic-admin"}}
	foreign, e := client.New(context.Background(), o.Projection, o.Credential, client.Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer foreign.Close()
	_, e = foreign.Query(context.Background(), "count()", false, nil)
	code(t, e, "forbidden")
	// A foreign ID also cannot escape the selected project through query arguments.
	_, e = admin.Query(context.Background(), `get("synthetic-other/`+task+`")`, false, nil)
	if e == nil {
		t.Fatal("path-shaped ID escaped parser")
	}
	found := false
	for _, el := range a.Elements {
		if el.ID == task {
			found = true
			if el.Status != "development" || el.AssignedTo != "synthetic-label" || el.Notes != "retained notes" || len(el.Checklist) != 1 || !el.Checklist[0].Checked {
				t.Fatalf("CLI facts %#v", el)
			}
		}
	}
	if !found {
		t.Fatal("created task absent")
	}
	h.close()
	again := startFixture(t, dir, false, data)
	persisted, e := again.app.Service.Snapshot(context.Background(), project)
	if e != nil || !reflect.DeepEqual(a, persisted) {
		t.Fatalf("CRUD restart %#v %v", persisted, e)
	}
	other, e := again.app.Service.Snapshot(context.Background(), "synthetic-other")
	if e != nil || !reflect.DeepEqual(b, other) {
		t.Fatalf("other restart %v", e)
	}
}

func legacyWithInertClaims(t *testing.T) ([]byte, []byte) {
	t.Helper()
	gr, e := gzip.NewReader(bytes.NewReader(fixture(t, "legacy-v2.tar.gz")))
	if e != nil {
		t.Fatal(e)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	claims := []byte(`{"actor":"forged-owner","signature":"inert-claim-only","event":"synthetic"}` + "\n")
	activityPath := ".activity/TASK-001/events.ndjson"
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		if h.Name == ".task-board-transfer.json" {
			b, e = json.Marshal(map[string]any{"format": "task-board", "version": 2, "activity": []map[string]string{{"element_id": "TASK-001", "path": activityPath, "sha256": digest(claims)}}})
			if e != nil {
				t.Fatal(e)
			}
		}
		if strings.HasSuffix(h.Name, "TASK-001_a/progress.md") {
			b = append(b, []byte("\n## run_id\nSYNTHETIC-RUN\n")...)
		}
		copyHeader := *h
		copyHeader.Size = int64(len(b))
		if e = tw.WriteHeader(&copyHeader); e != nil {
			t.Fatal(e)
		}
		if _, e = tw.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	if e = tw.WriteHeader(&tar.Header{Name: activityPath, Mode: 0600, Size: int64(len(claims)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); e != nil {
		t.Fatal(e)
	}
	if _, e = tw.Write(claims); e != nil {
		t.Fatal(e)
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = gz.Close(); e != nil {
		t.Fatal(e)
	}
	return archive.Bytes(), claims
}

func TestLegacyConsentDigestAndInertActorDock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	h := start(t, dir, false)
	c := h.client(t, "admin")
	archive, claims := legacyWithInertClaims(t)
	report, e := c.Import(context.Background(), archive, client.ImportOptions{DryRun: true})
	if e != nil {
		t.Fatal(e)
	}
	var got struct {
		Import struct {
			Exclusions []string `json:"exclusions"`
			Warnings   []string `json:"warnings"`
		} `json:"import"`
	}
	if e = json.Unmarshal(report.JSON, &got); e != nil {
		t.Fatal(e)
	}
	if report.Revision != 0 || !reflect.DeepEqual(got.Import.Exclusions, []string{"TASK-001:run_id"}) || len(got.Import.Warnings) != 1 {
		t.Fatalf("legacy report %s", report.JSON)
	}
	zero := contract.Revision(0)
	_, e = c.Import(context.Background(), archive, client.ImportOptions{SHA256: digest(archive), IfMatch: &zero})
	code(t, e, "invalid_snapshot")
	snap, e := h.app.Service.Snapshot(context.Background(), project)
	if e != nil || snap.Revision != 0 || len(snap.Elements) != 0 {
		t.Fatalf("unconsented import wrote: %v", e)
	}
	committed, e := c.Import(context.Background(), archive, client.ImportOptions{SHA256: digest(archive), IfMatch: &zero, AcceptExclusions: true})
	if e != nil || committed.Revision != 1 {
		t.Fatalf("consented import: %v", e)
	}
	snap, e = h.app.Service.Snapshot(context.Background(), project)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range snap.Provenance {
		if bytes.Equal(p.Data, claims) && p.SHA256 == digest(claims) {
			found = true
		}
	}
	if !found {
		t.Fatal("legacy inert claims were lost")
	}
	h.close()
	db, e := store.Open(filepath.Join(dir, project+".db"), contract.Project{ID: project}, false)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	events, e := db.Audit(context.Background())
	if e != nil || len(events) != 1 || events[0].Context.AccountID != "synthetic-admin" || events[0].Context.Operation != "import" {
		t.Fatalf("inert actor gained audit authority: %#v %v", events, e)
	}
}
