package deployment_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/deployment"
)

func config(t *testing.T) deployment.Config {
	t.Helper()
	// Keep AF_UNIX paths short even on long workbench paths.
	root, err := os.MkdirTemp("", "bd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err := os.Mkdir(filepath.Join(root, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	return deployment.Config{Profile: deployment.SingleTrustUnit, Endpoint: "unix://" + filepath.Join(root, "run", "s"), DataDir: filepath.Join(root, "data"), PolicyFile: filepath.Join(root, "policy", "release.json"), ExportDir: filepath.Join(root, "exports")}
}

func TestExplicitSelectionAndProductionCannotBeEnabled(t *testing.T) {
	for _, name := range []string{"", "production", "PRODUCTION", "single-trust-unit ", "remoteTLS", "synthetic", "unknown"} {
		_, err := deployment.Describe(name)
		if err == nil {
			t.Fatalf("accepted %q", name)
		}
		c := config(t)
		c.Profile = name
		if _, err := deployment.Validate(c); err == nil {
			t.Fatalf("validated %q", name)
		}
	}
	_, err := deployment.Describe("production")
	if !errors.Is(err, deployment.ErrProductionGated) || !strings.Contains(err.Error(), "A2-035") || !strings.Contains(err.Error(), "A2-036") {
		t.Fatal(err)
	}
	d, err := deployment.Describe(deployment.SingleTrustUnit)
	if err != nil {
		t.Fatal(err)
	}
	if d.Profile != "single-trust-unit" || d.PerAgentIsolation || d.ProductionAdmitted || d.ClientSelfSpawn || d.ClientAutoRecovery || d.RemoteTLSActivation || !d.RemoteTLSQualificationSupported || d.Transport != "uds-only" || !strings.Contains(d.Identity, "no per-agent isolation") || d.LifecycleOwner != "agents2 board orchestrator" {
		t.Fatalf("unsafe descriptor %+v", d)
	}
	d.ProductionBlockers[0] = "caller mutation"
	fresh, _ := deployment.Describe(deployment.SingleTrustUnit)
	if fresh.ProductionBlockers[0] == d.ProductionBlockers[0] {
		t.Fatal("mutable shared descriptor")
	}
}

func TestConfigRejectsRemoteActivationMalformedAndOverlappingPathsWithoutWrites(t *testing.T) {
	cases := map[string]func(*deployment.Config){
		"https":               func(c *deployment.Config) { c.Endpoint = "https://example.invalid" },
		"tcp":                 func(c *deployment.Config) { c.Endpoint = "http://127.0.0.1:1234" },
		"relative_socket":     func(c *deployment.Config) { c.Endpoint = "unix://relative" },
		"escaped_socket":      func(c *deployment.Config) { c.Endpoint += "%20" },
		"query_socket":        func(c *deployment.Config) { c.Endpoint += "?x=1" },
		"noncanonical_socket": func(c *deployment.Config) { c.Endpoint += "/../s" },
		"remote_tls":          func(c *deployment.Config) { c.RemoteTLS = true },
		"listen":              func(c *deployment.Config) { c.TCPListen = "127.0.0.1:0" },
		"cert":                func(c *deployment.Config) { c.TLSCertFile = "secret" },
		"key":                 func(c *deployment.Config) { c.TLSKeyFile = "secret" },
		"missing_data":        func(c *deployment.Config) { c.DataDir = "" },
		"relative_data":       func(c *deployment.Config) { c.DataDir = "data" },
		"root_data":           func(c *deployment.Config) { c.DataDir = "/" },
		"dot_data":            func(c *deployment.Config) { c.DataDir += "/../data" },
		"whitespace":          func(c *deployment.Config) { c.DataDir += "\n" },
		"invalid_utf8":        func(c *deployment.Config) { c.PolicyFile += string([]byte{255}) },
		"missing_runtime":     func(c *deployment.Config) { c.Endpoint = "unix://" + filepath.Join(c.DataDir, "run", "s") },
		"equal_data_export":   func(c *deployment.Config) { c.ExportDir = c.DataDir },
		"export_in_data":      func(c *deployment.Config) { c.ExportDir = filepath.Join(c.DataDir, "exports") },
		"data_in_export":      func(c *deployment.Config) { c.DataDir = filepath.Join(c.ExportDir, "data") },
		"policy_in_data":      func(c *deployment.Config) { c.PolicyFile = filepath.Join(c.DataDir, "p") },
		"policy_in_export":    func(c *deployment.Config) { c.PolicyFile = filepath.Join(c.ExportDir, "p") },
		"policy_in_runtime": func(c *deployment.Config) {
			c.PolicyFile = filepath.Join(filepath.Dir(strings.TrimPrefix(c.Endpoint, "unix://")), "p")
		},
		"data_in_runtime": func(c *deployment.Config) {
			c.DataDir = filepath.Join(filepath.Dir(strings.TrimPrefix(c.Endpoint, "unix://")), "data")
		},
		"runtime_in_export": func(c *deployment.Config) { c.ExportDir = filepath.Dir(strings.TrimPrefix(c.Endpoint, "unix://")) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := config(t)
			original := c
			mutate(&c)
			if _, err := deployment.Validate(c); err == nil {
				t.Fatal("unsafe config accepted")
			}
			for _, p := range []string{original.DataDir, original.ExportDir, filepath.Dir(original.PolicyFile)} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatalf("validation wrote %s: %v", p, err)
				}
			}
		})
	}
}

func TestAliasesExistingTypesAndComponentBoundaries(t *testing.T) {
	for _, resource := range []string{"data", "runtime", "policy", "exports", "socket"} {
		t.Run(resource, func(t *testing.T) {
			c := config(t)
			target := filepath.Join(filepath.Dir(c.DataDir), "other")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			var path string
			switch resource {
			case "data":
				path = c.DataDir
			case "runtime":
				path = filepath.Dir(strings.TrimPrefix(c.Endpoint, "unix://"))
				os.Remove(path)
			case "policy":
				path = filepath.Dir(c.PolicyFile)
			case "exports":
				path = c.ExportDir
			case "socket":
				path = strings.TrimPrefix(c.Endpoint, "unix://")
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if _, err := deployment.Validate(c); err == nil {
				t.Fatal("symlink accepted")
			}
		})
	}
	for _, resource := range []string{"data", "policy", "socket"} {
		t.Run("type_"+resource, func(t *testing.T) {
			c := config(t)
			path := c.DataDir
			if resource == "policy" {
				path = c.PolicyFile
				os.MkdirAll(filepath.Dir(path), 0700)
				os.Mkdir(path, 0700)
			} else {
				if resource == "socket" {
					path = strings.TrimPrefix(c.Endpoint, "unix://")
				}
				os.WriteFile(path, []byte("occupied"), 0600)
			}
			if _, err := deployment.Validate(c); err == nil {
				t.Fatal("wrong file type accepted")
			}
		})
	}
	c := config(t)
	c.ExportDir = c.DataDir + "-exports" // sibling prefix is not overlap
	if _, err := deployment.Validate(c); err != nil {
		t.Fatal(err)
	}
}

func TestPlanSnapshotsConfigAndBindsProjectionWithoutDialOrProvision(t *testing.T) {
	c := config(t)
	p, err := deployment.Validate(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Profile = "production"
	c.Endpoint = "https://example.invalid"
	saved, err := p.Config()
	if err != nil || saved.Profile != deployment.SingleTrustUnit {
		t.Fatal(saved, err)
	}
	tr, err := p.NewUnixTransport()
	if err != nil {
		t.Fatal(err)
	}
	tr.CloseIdleConnections()
	good := contract.Projection{Endpoint: saved.Endpoint, AccountID: "shared-agent", ProjectID: "board"}
	if err := p.CheckProjection(good); err != nil {
		t.Fatal(err)
	}
	good.Endpoint += "-other"
	if p.CheckProjection(good) == nil {
		t.Fatal("endpoint mismatch accepted")
	}
	good.Endpoint = saved.Endpoint
	good.AccountID = ""
	if p.CheckProjection(good) == nil {
		t.Fatal("invalid projection accepted")
	}
	for _, path := range []string{saved.DataDir, saved.PolicyFile, saved.ExportDir, strings.TrimPrefix(saved.Endpoint, "unix://")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("preflight created resource", path, err)
		}
	}
	var zero deployment.Plan
	if _, err := zero.Config(); err == nil {
		t.Fatal("zero config")
	}
	if _, err := zero.Descriptor(); err == nil {
		t.Fatal("zero descriptor")
	}
	if zero.WriteStatus(&bytes.Buffer{}) == nil {
		t.Fatal("zero status")
	}
	if _, err := zero.NewUnixTransport(); err == nil {
		t.Fatal("zero transport")
	}
	if zero.CheckProjection(good) == nil {
		t.Fatal("zero projection")
	}
	if _, err := zero.WithStatusHeaders(http.NotFoundHandler()); err == nil {
		t.Fatal("zero wrapper")
	}
}

func TestStatusMetadataIsSafeAndCannotTurnFailedReadinessGreen(t *testing.T) {
	c := config(t)
	p, err := deployment.Validate(c)
	if err != nil {
		t.Fatal(err)
	}
	var status bytes.Buffer
	if err := p.WriteStatus(&status); err != nil {
		t.Fatal(err)
	}
	var want deployment.Descriptor
	if err := json.Unmarshal(status.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{c.DataDir, c.PolicyFile, c.ExportDir, c.Endpoint, "account_id", "project_id"} {
		if strings.Contains(status.String(), secret) {
			t.Fatal("status leaked private input")
		}
	}
	for _, code := range []int{200, 503} {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{"status":"original"}`))
		})
		h, err := p.WithStatusHeaders(next)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range []string{"/ready", "/v1/version"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
			var got deployment.Descriptor
			if err := json.Unmarshal([]byte(w.Header().Get(deployment.DescriptorHeader)), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) || w.Code != code || w.Body.String() != `{"status":"original"}` {
				t.Fatalf("changed readiness response: %+v", w)
			}
		}
	}
	if p.WriteStatus(nil) == nil {
		t.Fatal("nil output")
	}
	if _, err := p.WithStatusHeaders(nil); err == nil {
		t.Fatal("nil handler")
	}
}

func TestClientProfileHasNoServerPrivatePathsOrRemoteActivation(t *testing.T) {
	projection := contract.Projection{Endpoint: "unix:///nonexistent-private-run/board.sock", AccountID: "shared-agent", ProjectID: "board"}
	tr, err := deployment.NewClientTransport(deployment.SingleTrustUnit, projection)
	if err != nil {
		t.Fatal(err)
	}
	tr.CloseIdleConnections()
	for _, profile := range []string{"", "production", "unknown"} {
		if _, err := deployment.NewClientTransport(profile, projection); err == nil {
			t.Fatal("client profile accepted", profile)
		}
	}
	for _, endpoint := range []string{"https://example.invalid", "http://127.0.0.1:1234", "unix://relative"} {
		bad := projection
		bad.Endpoint = endpoint
		if _, err := deployment.NewClientTransport(deployment.SingleTrustUnit, bad); err == nil {
			t.Fatal("client endpoint accepted", endpoint)
		}
	}
	projection.AccountID = ""
	if _, err := deployment.NewClientTransport(deployment.SingleTrustUnit, projection); err == nil {
		t.Fatal("invalid client projection accepted")
	}
	var out bytes.Buffer
	if err := deployment.WriteStatus("production", &out); !errors.Is(err, deployment.ErrProductionGated) || out.Len() != 0 {
		t.Fatal("production status", err, out.String())
	}
	if err := deployment.WriteStatus(deployment.SingleTrustUnit, &out); err != nil {
		t.Fatal(err)
	}
}
