package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

// Only the delivered response size varies. The synthetic peer admits one
// otherwise valid write; this does not establish a durable backend commit.
func TestWriteResponseBodyBound(t *testing.T) {
	for _, kind := range []string{"mutation", "import-commit"} {
		for _, extra := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/extra=%d", kind, extra), func(t *testing.T) {
				archive := archiveFixture(t)
				digest := archiveDigest(archive)
				query := "rename(TASK-a,name=x)"
				payload := `{"rename":{"id":"TASK-a","changed_ids":["TASK-a"]}}`
				if kind == "import-commit" {
					payload = `{"import":{"sha256":"` + digest + `","revision":13,"element_count":2}}`
				}
				var attempts atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if preflight(w, r) {
						return
					}
					attempts.Add(1)
					if r.Method != http.MethodPost || r.Header.Get("If-Match") != `"rev-12"` {
						t.Error("write lacks explicit fresh CAS or POST")
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if kind == "mutation" {
						if r.URL.RequestURI() != "/v1/boards/synthetic-board/q?mode=json" || string(body) != query {
							t.Error("mutation request changed")
						}
					} else if r.URL.RequestURI() != "/v1/boards/synthetic-board/import?dry_run=false" || !bytes.Equal(body, archive) || r.Header.Get("X-Snapshot-SHA256") != digest || r.Header.Get("Content-Type") != "application/gzip" {
						t.Error("import commit route/archive/pin changed")
					}
					w.Header().Set("ETag", `"rev-13"`)
					w.Header().Set("X-Request-ID", "synthetic-response-bound")
					w.Header().Set("Content-Length", fmt.Sprint(MaxJSON+extra))
					if _, err := io.WriteString(w, payload); err != nil {
						t.Error(err)
						return
					}
					chunk := bytes.Repeat([]byte(" "), 1<<20)
					for remaining := MaxJSON + extra - len(payload); remaining > 0; {
						n := min(remaining, len(chunk))
						if _, err := w.Write(chunk[:n]); err != nil {
							t.Error(err)
							return
						}
						remaining -= n
					}
				}))
				defer s.Close()
				c := newClient(t, s, nil)
				rev := contract.Revision(12)
				var got Response
				var err error
				if kind == "mutation" {
					got, err = c.Query(context.Background(), query, true, &rev)
				} else {
					got, err = c.Import(context.Background(), archive, ImportOptions{SHA256: digest, IfMatch: &rev})
				}
				if attempts.Load() != 1 {
					t.Fatalf("write replayed or not admitted: attempts=%d", attempts.Load())
				}
				if extra == 0 {
					if err != nil || got.Revision != 13 || got.RequestID != "synthetic-response-bound" || len(got.JSON) != MaxJSON {
						t.Fatalf("exact-limit valid response refused: revision=%d bytes=%d err=%v", got.Revision, len(got.JSON), err)
					}
					var parsed, want any
					if json.Unmarshal(got.JSON, &parsed) != nil || json.Unmarshal([]byte(payload), &want) != nil || !reflect.DeepEqual(parsed, want) {
						t.Fatal("valid response facts changed")
					}
				} else {
					e, ok := err.(*Error)
					if !ok || e.Code != "payload_too_large" || e.Status != 413 || e.Local || ExitCode(err) != 1 || e.RequestID != "synthetic-response-bound" {
						t.Fatalf("oversize write lost typed remote failure: %#v", err)
					}
					const guidance = "write response exceeds transport limit; outcome unknown; read and reconcile before another write"
					if e.Message != guidance || strings.Count(err.Error(), "read and reconcile") != 1 || !reflect.DeepEqual(got, Response{}) {
						t.Fatalf("oversize write lost empty result or single safe guidance: result bytes=%d err=%v", len(got.JSON), err)
					}
				}
				t.Logf("delivered_bytes=%d write_attempts=%d extra=%d", MaxJSON+extra, attempts.Load(), extra)
			})
		}
	}
}
