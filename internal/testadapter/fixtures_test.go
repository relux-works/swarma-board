package testadapter

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

// This verifies the handed-off fixture bytes/schema, not a transfer parser or
// product acceptance result. P4 supplies adversarial decoding tests separately.
func TestSharedNativeFixturePreservesStoredFacts(t *testing.T) {
	data, err := os.ReadFile("testdata/elements.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		SchemaVersion int                `json:"schema_version"`
		Elements      []contract.Element `json:"elements"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != 1 || len(document.Elements) != 7 {
		t.Fatal("fixture schema/count changed")
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := json.Unmarshal(data, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("stored fields changed on DTO roundtrip")
	}
	byID := map[string]contract.Element{}
	for _, e := range document.Elements {
		byID[e.ID] = e
	}
	if byID["TASK-002"].Estimate != nil || byID["TASK-003"].Estimate.Kind != "notEstimated" || byID["TASK-004"].Estimate.Scale.Number != 0 {
		t.Fatal("estimate distinctions lost")
	}
	if byID["TASK-002"].CreatedAt != nil || byID["TASK-001"].CreatedAt == nil {
		t.Fatal("nullable timestamps changed")
	}
	if !reflect.DeepEqual(byID["TASK-003"].BlockedBy, []string{"TASK-001", "TASK-002"}) {
		t.Fatal("DAG fixture changed")
	}

	var digests map[string]string
	readJSON(t, "testdata/archives.sha256.json", &digests)
	for _, name := range []string{"native-v1.tar.gz", "legacy-v2.tar.gz"} {
		archive, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(archive)
		if hex.EncodeToString(hash[:]) != digests[name] {
			t.Fatalf("archive digest differs: %s", name)
		}
		files := readFixtureArchive(t, archive)
		if name == "native-v1.tar.gz" {
			var manifest struct {
				Files []struct{ Path, SHA256 string } `json:"files"`
			}
			if err := json.Unmarshal(files[".swarma-board-transfer.json"], &manifest); err != nil {
				t.Fatal(err)
			}
			if len(files) != len(manifest.Files)+1 {
				t.Fatal("unlisted native fixture entry")
			}
			for _, entry := range manifest.Files {
				sum := sha256.Sum256(files[entry.Path])
				if hex.EncodeToString(sum[:]) != entry.SHA256 {
					t.Fatal("manifest digest mismatch")
				}
			}
			if !bytes.Equal(files["elements.json"], data) {
				t.Fatal("archive and standalone records differ")
			}
		} else {
			if len(files) != 15 {
				t.Fatal("legacy fixture entry count changed")
			}
			for name := range files {
				if name == ".task-board-transfer.json" {
					continue
				}
				for _, dir := range strings.Split(name, "/")[:len(strings.Split(name, "/"))-1] {
					if !strings.Contains(dir, "_") {
						t.Fatal("legacy directory is not ID_name")
					}
				}
			}
		}
	}
}

func readJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}
func readFixtureArchive(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := files[header.Name]; exists {
			t.Fatal("duplicate fixture entry")
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = body
	}
	return files
}
