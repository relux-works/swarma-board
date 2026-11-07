package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

const syntheticBearer = "synthetic-secret-only-in-memory"
const versionJSON = `{"product":"swarma-board","commit":"dev","contracts":{"swarma_board_api":"v1","task_plan":"v1","snapshot_import":"v1","revision_precondition":"v1"}}`
const planJSON = `{"plan":{"scope":{"id":"","type":"project","name":"Project","ids":[],"mode":"children","active":false},"usable":true,"summary":{"totalElements":2,"totalPhases":2},"phases":[{"phase":1,"elements":[{"id":"TASK-a","blockedBy":[]}],"containers":["EPIC-a","STORY-a"]},{"phase":2,"elements":[{"id":"TASK-b","blockedBy":["TASK-a"]}],"containers":["EPIC-a","STORY-a"]}],"criticalPath":["TASK-a","TASK-b"],"dependency_problems":[],"task_cycles":[],"projection_cycles":[]}}`

type credentialFunc func(context.Context, contract.Projection) (string, error)

func (f credentialFunc) Bearer(ctx context.Context, p contract.Projection) (string, error) {
	return f(ctx, p)
}
func credential() contract.CredentialSource {
	return credentialFunc(func(context.Context, contract.Projection) (string, error) { return syntheticBearer, nil })
}
func projection(endpoint string) contract.ProjectionSource {
	return testadapter.Projection{Value: contract.Projection{Endpoint: endpoint, ProjectID: "synthetic-board", AccountID: "synthetic-account"}}
}
func preflight(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/v1/version":
		io.WriteString(w, versionJSON)
		return true
	case "/v1/me":
		io.WriteString(w, `{"account_id":"synthetic-account"}`)
		return true
	}
	return false
}
func newClient(t *testing.T, s *httptest.Server, tlsConfig *tls.Config) *Client {
	t.Helper()
	c, err := New(context.Background(), projection(s.URL), credential(), Options{SyntheticHTTP: true, TLSConfig: tlsConfig})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func TestSamePlanOverLoopbackAndVerifiedTLS(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "literal-http", true: "verified-tls"}[secure], func(t *testing.T) {
			var paths []string
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.Header.Get("Authorization") != "Bearer "+syntheticBearer {
					t.Error("missing injected bearer")
				}
				if preflight(w, r) {
					return
				}
				if r.URL.Path != "/v1/boards/synthetic-board/q" || r.URL.RawQuery != "mode=json" || r.Method != "POST" {
					t.Error("wrong route")
				}
				b, _ := io.ReadAll(r.Body)
				if string(b) != "plan()" && string(b) != "get(TASK-a){full}" {
					t.Error("wrong query")
				}
				w.Header().Set("ETag", `"rev-7"`)
				w.Header().Set("X-Request-ID", "synthetic-request")
				if string(b) == "get(TASK-a){full}" {
					io.WriteString(w, `{"get":{"id":"TASK-a","title":"Synthetic task","blockedBy":[],"estimate":{"kind":"notEstimated"},"checklist":[],"notes":""}}`)
				} else {
					io.WriteString(w, planJSON)
				}
			})
			var s *httptest.Server
			var trust *tls.Config
			if secure {
				s = httptest.NewTLSServer(h)
				trust = s.Client().Transport.(*http.Transport).TLSClientConfig
			} else {
				s = httptest.NewServer(h)
			}
			defer s.Close()
			c := newClient(t, s, trust)
			r, err := c.Query(context.Background(), "plan()", false, nil)
			if err != nil {
				t.Fatal(err)
			}
			if string(r.JSON) != planJSON || r.Revision != 7 || r.RequestID != "synthetic-request" {
				t.Fatal("plan was transformed")
			}
			facts, err := c.Query(context.Background(), "get(TASK-a){full}", false, nil)
			if err != nil || !strings.Contains(string(facts.JSON), `"title":"Synthetic task"`) {
				t.Fatal("synthetic facts changed", err)
			}
			if !reflect.DeepEqual(paths, []string{"/v1/version", "/v1/me", "/v1/boards/synthetic-board/q", "/v1/boards/synthetic-board/q"}) {
				t.Fatal(paths)
			}
		})
	}
}
func TestProjectionFailClosed(t *testing.T) {
	valid := contract.Projection{Endpoint: "http://127.0.0.1:8000", ProjectID: "synthetic-board", AccountID: "synthetic-account"}
	for _, endpoint := range []string{"http://localhost:8000", "http://127.0.0.2", "http://127.0.0.1.evil", "ftp://127.0.0.1", "https://user:secret@example.test", "https://example.test/path", "https://example.test?token=secret", "https://example.test/#secret", "http://127.0.0.1:0", "http://127.0.0.1:99999", "http://127.0.0.1:", "http://2130706433", "http://[::ffff:127.0.0.1]"} {
		t.Run(endpoint, func(t *testing.T) {
			p := valid
			p.Endpoint = endpoint
			if ValidateProjection(p) == nil {
				t.Error("accepted unsafe endpoint")
			}
		})
	}
	for _, endpoint := range []string{"http://127.0.0.1:8000", "http://[::1]:8000", "https://example.test", "https://example.test/"} {
		p := valid
		p.Endpoint = endpoint
		if err := ValidateProjection(p); err != nil {
			t.Error(endpoint, err)
		}
	}
	for _, project := range []string{"", "../board", "a%2fb", "A", "a/b", strings.Repeat("a", 65)} {
		p := valid
		p.ProjectID = project
		if ValidateProjection(p) == nil {
			t.Error("accepted invalid project")
		}
	}
	calls := 0
	_, err := New(context.Background(), projection("http://localhost"), credentialFunc(func(context.Context, contract.Projection) (string, error) { calls++; return syntheticBearer, nil }), Options{SyntheticHTTP: true})
	if err == nil || calls != 0 {
		t.Fatal("credential accessed before projection validation")
	}
	for _, pair := range []struct {
		p contract.ProjectionSource
		c contract.CredentialSource
	}{{nil, credential()}, {projection(valid.Endpoint), nil}} {
		if _, err := New(context.Background(), pair.p, pair.c, Options{SyntheticHTTP: true}); ExitCode(err) != 2 {
			t.Fatal("missing adapter did not fail locally")
		}
	}
}
func TestPreflightFailureNeverReachesProject(t *testing.T) {
	for _, kind := range []string{"missing-capability", "wrong-product", "bad-json", "wrong-account", "unauthorized", "invalid-credential"} {
		t.Run(kind, func(t *testing.T) {
			projectCalls := 0
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/v1/version":
					v := versionJSON
					if kind == "missing-capability" {
						v = strings.Replace(v, `"task_plan":"v1"`, `"task_plan":"v2"`, 1)
					}
					if kind == "wrong-product" {
						v = strings.Replace(v, "swarma-board", "other", 1)
					}
					if kind == "bad-json" {
						v = "{"
					}
					io.WriteString(w, v)
				case "/v1/me":
					if kind == "unauthorized" {
						w.WriteHeader(401)
						io.WriteString(w, `{"error":{"code":"unauthorized","message":"`+syntheticBearer+`","details":{"token":"`+syntheticBearer+`"}},"request_id":"`+syntheticBearer+`"}`)
					} else {
						account := "synthetic-account"
						if kind == "wrong-account" {
							account = "other"
						}
						json.NewEncoder(w).Encode(map[string]string{"account_id": account})
					}
				default:
					projectCalls++
				}
			}))
			defer s.Close()
			cred := credential()
			if kind == "invalid-credential" {
				cred = credentialFunc(func(context.Context, contract.Projection) (string, error) { return "", nil })
			}
			c, err := New(context.Background(), projection(s.URL), cred, Options{SyntheticHTTP: true})
			if c != nil || err == nil || projectCalls != 0 {
				t.Fatalf("unsafe preflight: %v calls=%d", err, projectCalls)
			}
			b, _ := json.Marshal(err)
			if strings.Contains(string(b), syntheticBearer) {
				t.Fatal("credential leaked")
			}
			if kind == "invalid-credential" && calls != 0 {
				t.Fatal("network before credential validation")
			}
		})
	}
}
func TestRedirectNeverForwardsBearer(t *testing.T) {
	for _, route := range []string{"version", "me", "project"} {
		t.Run(route, func(t *testing.T) {
			forwarded := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++ }))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirect := route == "version" || route == "me" && r.URL.Path == "/v1/me" || route == "project" && strings.Contains(r.URL.Path, "/boards/")
				if redirect {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				preflight(w, r)
			}))
			defer source.Close()
			c, err := New(context.Background(), projection(source.URL), credential(), Options{SyntheticHTTP: true})
			if err == nil {
				defer c.Close()
				_, err = c.Query(context.Background(), "count()", false, nil)
			}
			if err == nil || forwarded != 0 {
				t.Fatalf("redirect followed: calls=%d err=%v", forwarded, err)
			}
		})
	}
}
func TestTLSVerificationRequired(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { preflight(w, r) }))
	defer s.Close()
	if _, err := New(context.Background(), projection(s.URL), credential(), Options{SyntheticHTTP: true}); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	if _, err := New(context.Background(), projection(s.URL), credential(), Options{SyntheticHTTP: true, TLSConfig: &tls.Config{InsecureSkipVerify: true}}); ExitCode(err) != 2 {
		t.Fatal("insecure option accepted")
	}
}
func TestWriteRevisionAndNoRetry(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, status := range []int{200, 409, 412, 0} {
			t.Run(strconv.Itoa(status)+strconvBool(explicit), func(t *testing.T) {
				var queries []string
				var tags []string
				var mu sync.Mutex
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if preflight(w, r) {
						return
					}
					b, _ := io.ReadAll(r.Body)
					mu.Lock()
					queries = append(queries, string(b))
					tags = append(tags, r.Header.Get("If-Match"))
					mu.Unlock()
					w.Header().Set("ETag", `"rev-8"`)
					if string(b) == "count()" {
						io.WriteString(w, `{"count":2}`)
						return
					}
					if status == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							conn.Close()
						}
						return
					}
					w.WriteHeader(status)
					if status != 200 {
						io.WriteString(w, `{"error":{"code":"revision_conflict","message":"secret text","details":{"current_revision":9}}}`)
					} else {
						io.WriteString(w, `{"rename":{"id":"TASK-a","changed_ids":["TASK-a"]}}`)
					}
				}))
				defer s.Close()
				c := newClient(t, s, nil)
				var expected *contract.Revision
				if explicit {
					n := contract.Revision(3)
					expected = &n
				}
				_, err := c.Query(context.Background(), `rename(TASK-a,name="next")`, true, expected)
				want := []string{"count()", `rename(TASK-a,name="next")`}
				wantTags := []string{"", `"rev-8"`}
				if explicit {
					want = want[1:]
					wantTags = []string{`"rev-3"`}
				}
				mu.Lock()
				gotQueries := append([]string(nil), queries...)
				gotTags := append([]string(nil), tags...)
				mu.Unlock()
				if !reflect.DeepEqual(gotQueries, want) || !reflect.DeepEqual(gotTags, wantTags) {
					t.Fatal(gotQueries, gotTags)
				}
				if status == 200 {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil {
					t.Fatal("failed write succeeded")
				} else if status == 0 {
					if !strings.Contains(err.Error(), "read and reconcile") {
						t.Fatal(err)
					}
				} else if ExitCode(err) != 4 {
					t.Fatal("wrong conflict exit", err)
				}
			})
		}
	}
}
func strconvBool(v bool) string {
	if v {
		return "-explicit"
	}
	return "-preflight"
}
func TestInBandErrorSanitization(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preflight(w, r) {
			return
		}
		w.Header().Set("X-Request-ID", syntheticBearer)
		io.WriteString(w, `{"error":{"code":"blocked","message":"`+syntheticBearer+` /private/path","details":{"query":"sensitive","current_revision":12}}}`)
	}))
	defer s.Close()
	c := newClient(t, s, nil)
	_, err := c.Query(context.Background(), "count()", false, nil)
	if err == nil {
		t.Fatal("200 error succeeded")
	}
	b, _ := json.Marshal(err)
	if strings.Contains(string(b), syntheticBearer) || strings.Contains(string(b), "private/path") || strings.Contains(string(b), "sensitive") {
		t.Fatal(string(b))
	}
	if !strings.Contains(string(b), `"current_revision":12`) {
		t.Fatal("safe revision lost")
	}
}
func TestETag(t *testing.T) {
	for _, bad := range []string{"", `rev-1`, `W/"rev-1"`, `*`, `"rev-1", "rev-2"`, `"rev--1"`, `"rev-01"`, `"rev-18446744073709551616"`, `"rev-+1"`} {
		if _, err := ParseETag(bad); err == nil {
			t.Error(bad)
		}
	}
	for _, n := range []contract.Revision{0, 1, 18446744073709551615} {
		got, err := ParseETag(ETag(n))
		if got != n || err != nil {
			t.Fatal(n, got, err)
		}
	}
}
func TestCanceledRequestIsBounded(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { preflight(w, r) }))
	defer s.Close()
	c := newClient(t, s, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Query(ctx, "count()", false, nil)
	if err == nil {
		t.Fatal("canceled request succeeded")
	}
}

func TestAmbiguousPreflightFailsBeforeProject(t *testing.T) {
	for _, body := range []string{strings.Replace(versionJSON, `"product":`, `"product":"other","product":`, 1), strings.Replace(versionJSON, `"task_plan":"v1"`, `"task_plan":"v2","task_plan":"v1"`, 1), strings.Replace(versionJSON, `"commit":"dev"`, `"commit":"untrusted-text"`, 1)} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/version" {
				t.Error("ambiguous version reached another route")
			}
			io.WriteString(w, body)
		}))
		_, err := New(context.Background(), projection(s.URL), credential(), Options{SyntheticHTTP: true})
		s.Close()
		if ExitCode(err) != 2 {
			t.Fatal("ambiguous capability/version not rejected locally", err)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/version" {
			io.WriteString(w, versionJSON)
			return
		}
		if r.URL.Path != "/v1/me" {
			t.Error("ambiguous identity reached project")
		}
		io.WriteString(w, `{"account_id":"other","account_id":"synthetic-account"}`)
	}))
	defer s.Close()
	if _, err := New(context.Background(), projection(s.URL), credential(), Options{SyntheticHTTP: true}); ExitCode(err) != 2 {
		t.Fatal("ambiguous account accepted", err)
	}
}
func TestAmbiguousRevisionNeverAuthorizesWrite(t *testing.T) {
	for _, tag := range []string{"missing", "duplicate", "weak"} {
		t.Run(tag, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if preflight(w, r) {
					return
				}
				calls++
				if tag == "duplicate" {
					w.Header().Add("ETag", `"rev-1"`)
					w.Header().Add("ETag", `"rev-2"`)
				}
				if tag == "weak" {
					w.Header().Set("ETag", `W/"rev-1"`)
				}
				io.WriteString(w, `{"count":1}`)
			}))
			defer s.Close()
			c := newClient(t, s, nil)
			_, err := c.Query(context.Background(), `rename(TASK-a,name=x)`, true, nil)
			if ExitCode(err) != 2 || calls != 1 {
				t.Fatal("write proceeded after bad ETag", calls, err)
			}
		})
	}
}
func TestTransportDoesNotInheritAmbientDefaults(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { preflight(w, r) }))
	defer s.Close()
	old := http.DefaultTransport
	http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer func() { http.DefaultTransport = old }()
	c := newClient(t, s, nil)
	tr := c.http.Transport.(*http.Transport)
	if tr.Proxy != nil || (tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify) || tr.ResponseHeaderTimeout.Seconds() != 305 || tr.MaxResponseHeaderBytes != 32<<10 {
		t.Fatal("ambient defaults inherited or timeout/header limits wrong")
	}
}
