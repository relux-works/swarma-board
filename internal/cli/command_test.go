package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

const cliToken = "synthetic-cli-token"
const cliVersion = `{"product":"swarma-board","commit":"dev","contracts":{"swarma_board_api":"v1","task_plan":"v1","snapshot_import":"v1","revision_precondition":"v1"}}`
const cliPlan = `{"plan":{"scope":{"id":"","type":"project","name":"Project","ids":[],"mode":"children","active":false},"usable":true,"summary":{"totalElements":2,"totalPhases":2},"phases":[{"phase":1,"elements":[{"id":"TASK-a"}],"containers":["STORY-a"]},{"phase":2,"elements":[{"id":"TASK-b","blockedBy":["TASK-a"]}],"containers":["STORY-a"]}],"criticalPath":["TASK-a","TASK-b"],"dependency_problems":[],"task_cycles":[],"projection_cycles":[]}}`

type cliCredential struct{}

func (cliCredential) Bearer(context.Context, contract.Projection) (string, error) {
	return cliToken, nil
}
func optionsFor(s *httptest.Server) Options {
	return Options{SyntheticHTTP: true, Projection: testadapter.Projection{Value: contract.Projection{Endpoint: s.URL, ProjectID: "synthetic-board", AccountID: "synthetic-account"}}, Credential: cliCredential{}}
}
func runTest(args []string, o Options) (int, string, string) {
	var out, err bytes.Buffer
	o.Out = &out
	o.Err = &err
	code := Run(context.Background(), args, o)
	return code, out.String(), err.String()
}
func cliPreflight(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/v1/version" {
		io.WriteString(w, cliVersion)
		return true
	}
	if r.URL.Path == "/v1/me" {
		io.WriteString(w, `{"account_id":"synthetic-account"}`)
		return true
	}
	return false
}
func TestLocalValidationDoesNotResolveAdapters(t *testing.T) {
	for _, args := range [][]string{{"q", `set_status(TASK-a,status=done)`}, {"m", `count()`}, {"q", `get(TASK-a);count()`}, {"m", `delete(TASK-a)`}, {"m", `rename(TASK-a,name=x)`, "--if-match", "-1"}, {"m", `rename(TASK-a,name=x)`, "--if-match", "18446744073709551616"}, {"plan", "TASK-a", "--scope", "TASK-b"}, {"plan", "--critical-path", "--phase", "1"}, {"plan", "--phase", "0"}, {"plan", "--scope", ""}, {"plan", "--scope", "TASK-a,,TASK-b"}, {"plan", "--mode", "execution"}, {"q"}, {"q", "count()", "extra"}, {"--not-a-flag=private-body"}, {"spawn", "private-body"}, {"q", `get("private-body)`}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, out, err := runTest(args, Options{})
			if code != 2 || out != "" {
				t.Fatal(code, out, err)
			}
			if !json.Valid([]byte(err)) || strings.Contains(err, "private-body") || strings.Contains(err, "adapter") {
				t.Fatal("local validation unsafe or ran too late", err)
			}
		})
	}
}
func TestPlanViewsUseOneRead(t *testing.T) {
	cases := []struct {
		args        []string
		query, want string
	}{{[]string{"plan", "--json"}, "plan()", cliPlan}, {[]string{"plan", "--critical-path", "--json"}, "plan()", `{"criticalPath":["TASK-a","TASK-b"]}`}, {[]string{"plan", "--phase", "2", "--json"}, "plan()", `{"phase":2,"elements":[{"id":"TASK-b","blockedBy":["TASK-a"]}],"containers":["STORY-a"]}`}, {[]string{"plan", "--scope", "TASK-a,TASK-b", "--mode", "related", "--active", "--json"}, `plan(scope=["TASK-a","TASK-b"],mode=related,active=true)`, cliPlan}}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			reads := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if cliPreflight(w, r) {
					return
				}
				reads++
				b, _ := io.ReadAll(r.Body)
				if string(b) != tc.query {
					t.Error(string(b))
				}
				w.Header().Set("ETag", `"rev-5"`)
				io.WriteString(w, cliPlan)
			}))
			defer s.Close()
			code, out, err := runTest(tc.args, optionsFor(s))
			if code != 0 || reads != 1 || err != "" {
				t.Fatal(code, reads, err)
			}
			var got, want any
			json.Unmarshal([]byte(out), &got)
			json.Unmarshal([]byte(tc.want), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatal(out)
			}
		})
	}
}
func TestUnusablePlanDiagnosticAndExit(t *testing.T) {
	diagnostic := strings.Replace(cliPlan, `"usable":true`, `"usable":false`, 1)
	for _, args := range [][]string{{"plan", "--critical-path", "--json"}, {"q", "plan()", "--json"}} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cliPreflight(w, r) {
				return
			}
			w.Header().Set("ETag", `"rev-5"`)
			w.Header().Set("X-Request-ID", "synthetic-diagnostic")
			io.WriteString(w, diagnostic)
		}))
		code, out, err := runTest(args, optionsFor(s))
		s.Close()
		if code != 1 || !strings.Contains(out, `"usable":false`) || !strings.Contains(err, `"request_id":"synthetic-diagnostic"`) {
			t.Fatal(code, out, err)
		}
	}
}
func TestExitMappingAndSafeEnvelope(t *testing.T) {
	for _, status := range []int{401, 403, 409, 412, 422, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if cliPreflight(w, r) {
					return
				}
				w.Header().Set("X-Request-ID", cliToken)
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"error":{"message":%q,"details":{"query":"private-body"}}}`, cliToken)
			}))
			defer s.Close()
			code, out, err := runTest([]string{"q", "count()", "--json"}, optionsFor(s))
			want := 1
			if status == 401 || status == 403 {
				want = 3
			}
			if status == 409 || status == 412 {
				want = 4
			}
			if code != want || out != "" || !json.Valid([]byte(err)) || strings.Contains(err, cliToken) || strings.Contains(err, "private-body") {
				t.Fatal(code, out, err)
			}
		})
	}
}
func TestHelpInventoryAndNoAmbientAdapter(t *testing.T) {
	root := NewCommand(Options{})
	var commands []string
	for _, cmd := range root.Commands() {
		commands = append(commands, cmd.Name())
	}
	sort.Strings(commands)
	if !reflect.DeepEqual(commands, []string{"export", "import", "m", "plan", "q", "version"}) {
		t.Fatal(commands)
	}
	for _, args := range [][]string{{"--help"}, {"plan", "--help"}, {"version", "--json"}} {
		code, out, err := runTest(args, Options{})
		if code != 0 || out == "" || err != "" {
			t.Fatal(code, out, err)
		}
		for _, forbidden := range []string{"--token", "--insecure", "--board-dir", "--config", "--autostart", "--provider"} {
			if strings.Contains(out, forbidden) {
				t.Fatal(out)
			}
		}
	}
	code, out, err := runTest([]string{"q", "count()"}, Options{})
	if code != 2 || out != "" || !strings.Contains(err, "adapters are required") {
		t.Fatal(code, out, err)
	}
}
func TestSyntheticFlagsUseExplicitFD(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+cliToken {
			t.Error("wrong fd token")
		}
		if cliPreflight(w, r) {
			return
		}
		w.Header().Set("ETag", `"rev-2"`)
		io.WriteString(w, `{"count":2}`)
	}))
	defer s.Close()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "projection.json")
	b, _ := json.Marshal(map[string]string{"endpoint": s.URL, "project_id": "synthetic-board", "account_id": "synthetic-account"})
	if err := os.WriteFile(fixture, b, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(dir, "credential")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(cliToken + "\n")
	f.Seek(0, 0)
	fd, dupErr := syscall.Dup(int(f.Fd()))
	if dupErr != nil {
		f.Close()
		t.Fatal(dupErr)
	}
	f.Close()
	var original syscall.Stat_t
	if err := syscall.Fstat(fd, &original); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runTest([]string{"q", "count()", "--json", "--synthetic-projection", fixture, "--synthetic-credential-fd", fmt.Sprint(fd)}, Options{})
	if code != 0 || !strings.Contains(out, `"count":2`) || stderr != "synthetic fixture mode\n" {
		t.Fatal(code, out, stderr)
	}
	var after syscall.Stat_t
	fdErr := syscall.Fstat(fd, &after)
	if fdErr != syscall.EBADF && (fdErr != nil || after.Dev == original.Dev && after.Ino == original.Ino) {
		t.Fatal("credential fd still refers to credential file", fdErr)
	}
}
func TestExportPublishesPrivateCompleteArchiveWithoutOverwrite(t *testing.T) {
	archive := nativeFixture(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cliPreflight(w, r) {
			return
		}
		w.Header().Set("ETag", `"rev-4"`)
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("X-Snapshot-SHA256", digest(archive))
		w.Write(archive)
	}))
	defer s.Close()
	path := filepath.Join(t.TempDir(), "export.tar.gz")
	code, _, err := runTest([]string{"export", path}, optionsFor(s))
	if code != 0 {
		t.Fatal(code, err)
	}
	got, _ := os.ReadFile(path)
	st, _ := os.Stat(path)
	if !bytes.Equal(got, archive) || st.Mode().Perm() != 0600 {
		t.Fatal("unsafe export")
	}
	code, _, _ = runTest([]string{"export", path}, Options{})
	if code != 2 {
		t.Fatal("existing export not refused before adapters")
	}
	symlink := path + "-link"
	os.Symlink(path, symlink)
	code, _, _ = runTest([]string{"export", symlink}, Options{})
	if code != 2 {
		t.Fatal("symlink destination accepted")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 2 {
		t.Fatal("temporary export file leaked")
	}
}
func TestImportCLIDigestAndConsent(t *testing.T) {
	archive := nativeFixture(t)
	path := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	os.WriteFile(path, archive, 0600)
	for _, args := range [][]string{{"import", path}, {"import", path, "--sha256", "bad"}, {"import", path, "--dry-run", "--accept-exclusions"}, {"import", path, "--dry-run", "--if-match", "0"}} {
		code, _, err := runTest(args, Options{})
		if code != 2 || strings.Contains(err, "adapter") {
			t.Fatal(code, err)
		}
	}
	for _, dry := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cliPreflight(w, r) {
				return
			}
			if r.URL.Path != "/v1/boards/synthetic-board/import" {
				t.Error("explicit if-match should skip count")
			}
			got, _ := io.ReadAll(r.Body)
			if !bytes.Equal(got, archive) {
				t.Error("archive changed")
			}
			w.Header().Set("ETag", `"rev-1"`)
			if dry {
				fmt.Fprintf(w, `{"import":{"sha256":%q,"element_count":0,"format":"swarma-board","version":1,"exclusions":["model"],"warnings":[]}}`, digest(archive))
			} else {
				if r.Header.Get("X-Import-Accept-Exclusions") != "true" || r.Header.Get("If-Match") != `"rev-0"` {
					t.Error("missing consent/revision")
				}
				fmt.Fprintf(w, `{"import":{"sha256":%q,"revision":1,"element_count":0}}`, digest(archive))
			}
		}))
		args := []string{"import", path, "--sha256", digest(archive), "--json"}
		if dry {
			args = append(args, "--dry-run")
		} else {
			args = append(args, "--if-match", "0", "--accept-exclusions")
		}
		code, out, err := runTest(args, optionsFor(s))
		s.Close()
		if code != 0 || !strings.Contains(out, digest(archive)) {
			t.Fatal(code, out, err)
		}
	}
}

func nativeFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../testadapter/testdata/native-v1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type rejectedOutput struct{}

func (rejectedOutput) Write([]byte) (int, error) { return 0, fmt.Errorf("private output failure") }
func TestOutputFailureUsesOtherErrorExit(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"version"}, Options{Out: rejectedOutput{}, Err: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), `"code":"output_error"`) || strings.Contains(stderr.String(), "private output failure") {
		t.Fatal(code, stderr.String())
	}
}
func TestIncompletePlanIsContractError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cliPreflight(w, r) {
			return
		}
		w.Header().Set("ETag", `"rev-1"`)
		io.WriteString(w, `{"plan":{"usable":true}}`)
	}))
	defer s.Close()
	for _, args := range [][]string{{"plan", "--critical-path"}, {"q", "plan()"}} {
		code, out, err := runTest(args, optionsFor(s))
		if code != 2 || out != "" || !strings.Contains(err, "incomplete plan") {
			t.Fatal(code, out, err)
		}
	}
}

func TestCLIParityAcrossLoopbackAndVerifiedTLS(t *testing.T) {
	archive := nativeFixture(t)
	dir := t.TempDir()
	var reference []string
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "tls"
		}
		t.Run(name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+cliToken {
					t.Error("wrong injected credential")
				}
				if cliPreflight(w, r) {
					return
				}
				w.Header().Set("ETag", `"rev-7"`)
				if r.URL.Path == "/v1/boards/synthetic-board/export" {
					w.Header().Set("Content-Type", "application/gzip")
					w.Header().Set("X-Snapshot-SHA256", digest(archive))
					w.Write(archive)
					return
				}
				b, _ := io.ReadAll(r.Body)
				switch string(b) {
				case "get(TASK-a){full}":
					io.WriteString(w, `{"get":{"id":"TASK-a","title":"Synthetic task","blockedBy":[],"notes":"Synthetic notes","checklist":[],"estimate":{"kind":"notEstimated"}}}`)
				case "plan()":
					io.WriteString(w, cliPlan)
				default:
					t.Error("unexpected query")
				}
			})
			var s *httptest.Server
			if secure {
				s = httptest.NewTLSServer(h)
			} else {
				s = httptest.NewServer(h)
			}
			defer s.Close()
			o := optionsFor(s)
			if secure {
				o.TLSConfig = s.Client().Transport.(*http.Transport).TLSClientConfig
			}
			path := filepath.Join(dir, name+".tar.gz")
			var results []string
			for _, args := range [][]string{{"q", "get(TASK-a){full}", "--json"}, {"plan", "--json"}, {"export", path, "--json"}} {
				code, out, err := runTest(args, o)
				if code != 0 || err != "" {
					t.Fatal(code, out, err)
				}
				results = append(results, out)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, archive) {
				t.Fatal("archive changed between endpoint styles", err)
			}
			if !secure {
				reference = results
			} else if !reflect.DeepEqual(reference, results) {
				t.Fatal("CLI behavior differs between HTTP and TLS", results)
			}
		})
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("unexpected local files", entries, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".task-board")); !os.IsNotExist(err) {
		t.Fatal("local board fallback created")
	}
}
