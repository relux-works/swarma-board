package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

func archiveFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../testadapter/testdata/native-v1.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func archiveDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func TestImportDigestRevisionAndConsent(t *testing.T) {
	b := archiveFixture(t)
	digest := archiveDigest(b)
	for _, dry := range []bool{false, true} {
		for _, accept := range []bool{false, true} {
			t.Run(fmt.Sprintf("dry=%v/accept=%v", dry, accept), func(t *testing.T) {
				var routes []string
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if preflight(w, r) {
						return
					}
					routes = append(routes, r.URL.RequestURI())
					w.Header().Set("ETag", `"rev-12"`)
					if strings.HasSuffix(r.URL.Path, "/q") {
						query, _ := io.ReadAll(r.Body)
						if string(query) != "count()" {
							t.Error("bad preflight")
						}
						io.WriteString(w, `{"count":0}`)
						return
					}
					got, _ := io.ReadAll(r.Body)
					if !bytes.Equal(got, b) || r.Header.Get("Content-Type") != "application/gzip" || r.Header.Get("X-Snapshot-SHA256") != digest {
						t.Error("archive/digest mismatch")
					}
					if r.URL.Query().Get("dry_run") != fmt.Sprint(dry) {
						t.Error("wrong dry run")
					}
					if dry {
						if r.Header.Get("If-Match") != "" || r.Header.Get("X-Import-Accept-Exclusions") != "" {
							t.Error("dry run had commit headers")
						}
						io.WriteString(w, `{"import":{"sha256":"`+digest+`","format":"swarma-board","version":1,"element_count":2,"exclusions":["model"],"warnings":[]}}`)
					} else {
						if r.Header.Get("If-Match") != `"rev-12"` {
							t.Error("missing import precondition")
						}
						want := ""
						if accept {
							want = "true"
						}
						if r.Header.Get("X-Import-Accept-Exclusions") != want {
							t.Error("implicit exclusion consent")
						}
						w.Header().Set("ETag", `"rev-13"`)
						io.WriteString(w, `{"import":{"sha256":"`+digest+`","revision":13,"element_count":2}}`)
					}
				}))
				defer s.Close()
				c := newClient(t, s, nil)
				_, err := c.Import(context.Background(), b, ImportOptions{DryRun: dry, SHA256: digest, AcceptExclusions: accept})
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"/v1/boards/synthetic-board/q?mode=json", "/v1/boards/synthetic-board/import?dry_run=false"}
				if dry {
					want = []string{"/v1/boards/synthetic-board/import?dry_run=true"}
				}
				if !reflect.DeepEqual(routes, want) {
					t.Fatal(routes)
				}
			})
		}
	}
}
func TestImportInvalidDigestNeverRequestsProject(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !preflight(w, r) {
			t.Error("invalid digest sent project request")
		}
	}))
	defer s.Close()
	c := newClient(t, s, nil)
	b := archiveFixture(t)
	for _, o := range []ImportOptions{{}, {SHA256: strings.Repeat("a", 64)}, {SHA256: "BAD"}, {DryRun: true, SHA256: strings.Repeat("b", 64)}} {
		if _, err := c.Import(context.Background(), b, o); ExitCode(err) != 2 {
			t.Fatal("digest not rejected locally", err)
		}
	}
}
func TestImportExplicitRevisionAndStaleNoRetry(t *testing.T) {
	b := archiveFixture(t)
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preflight(w, r) {
			return
		}
		calls++
		if r.URL.Path != "/v1/boards/synthetic-board/import" || r.Header.Get("If-Match") != `"rev-2"` {
			t.Error("unexpected preflight/route")
		}
		w.WriteHeader(412)
		io.WriteString(w, `{"error":{"code":"revision_conflict","details":{"current_revision":3}}}`)
	}))
	defer s.Close()
	c := newClient(t, s, nil)
	n := contract.Revision(2)
	_, err := c.Import(context.Background(), b, ImportOptions{SHA256: archiveDigest(b), IfMatch: &n})
	if ExitCode(err) != 4 || calls != 1 {
		t.Fatal(calls, err)
	}
}
func TestVerifiedExportAndBadResponse(t *testing.T) {
	b := archiveFixture(t)
	for _, kind := range []string{"valid", "bad-digest", "bad-type", "missing-etag", "in-band-error"} {
		t.Run(kind, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if preflight(w, r) {
					return
				}
				if r.URL.Path != "/v1/boards/synthetic-board/export" || r.Method != "GET" {
					t.Error("wrong export route")
				}
				w.Header().Set("Content-Type", "application/gzip")
				w.Header().Set("ETag", `"rev-14"`)
				w.Header().Set("X-Snapshot-SHA256", archiveDigest(b))
				switch kind {
				case "bad-digest":
					w.Header().Set("X-Snapshot-SHA256", strings.Repeat("f", 64))
				case "bad-type":
					w.Header().Set("Content-Type", "text/plain")
				case "missing-etag":
					w.Header().Del("ETag")
				case "in-band-error":
					io.WriteString(w, `{"error":{"code":"forbidden","message":"private"}}`)
					return
				}
				w.Write(b)
			}))
			defer s.Close()
			c := newClient(t, s, nil)
			got, rev, err := c.Export(context.Background())
			if kind == "valid" {
				if err != nil || rev != 14 || !bytes.Equal(got, b) {
					t.Fatal(rev, err)
				}
			} else if err == nil {
				t.Fatal("bad export accepted")
			}
		})
	}
}
func TestResponseBodyBound(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preflight(w, r) {
			return
		}
		w.Header().Set("ETag", `"rev-1"`)
		chunk := bytes.Repeat([]byte(" "), 1<<20)
		for n := 0; n < 65; n++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer s.Close()
	c := newClient(t, s, nil)
	_, err := c.Query(context.Background(), "count()", false, nil)
	e, ok := err.(*Error)
	if !ok || e.Code != "payload_too_large" {
		t.Fatal("oversized response accepted", err)
	}
}
func TestMalformedSuccessAfterWriteMeansUnknownOutcome(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"rename":null}`, `{"rename":{"id":"TASK-a"}}`, `{"rename":{"id":"TASK-a","changed_ids":[]},"extra":true}`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if preflight(w, r) {
					return
				}
				w.Header().Set("ETag", `"rev-4"`)
				io.WriteString(w, body)
			}))
			defer s.Close()
			c := newClient(t, s, nil)
			rev := contract.Revision(3)
			_, err := c.Query(context.Background(), `rename(TASK-a,name=x)`, true, &rev)
			if err == nil || !strings.Contains(err.Error(), "read and reconcile") {
				t.Fatal(err)
			}
		})
	}
}
