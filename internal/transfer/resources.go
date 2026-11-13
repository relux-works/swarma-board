package transfer

import (
	"encoding/json"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/relux-works/swarma-board/internal/contract"
)

const resourceMapPath = "provenance/legacy-resource-map-v1.json"
const resourceMapSchema = "swarma-board/legacy-resource-mapping/1"
const resourceURNPrefix = "urn:swarma-board:resource:sha256:"

type legacyResourceEntry struct {
	ElementID       string `json:"element_id"`
	ResourceOrdinal int    `json:"resource_ordinal"`
	OriginalURI     string `json:"original_uri"`
	OriginalPath    string `json:"original_path"`
	NativeURN       string `json:"native_urn"`
	ProvenancePath  string `json:"provenance_path"`
	SHA256          string `json:"sha256"`
	Bytes           int64  `json:"bytes"`
}
type legacyResourceMap struct {
	Schema  string                `json:"schema"`
	Entries []legacyResourceEntry `json:"entries"`
}

func confirmedLegacyResources(files map[string][]byte) bool {
	if _, native := files[nativeManifestPath]; native {
		return false
	}
	var m legacyManifest
	return strictJSON(files[legacyManifestPath], &m) == nil && m.Format == "task-board" && m.Version == 2
}

// Colon labels are inert only inside an owning element's reserved namespace.
// The structural root retains safePath's absolute/URI/drive/UNC refusals. No
// original pathname is ever extracted or used as a host filesystem name.
func legacyResourcePath(p string) (owner, relative string, ok bool) {
	index := strings.Index(p, "/.resources/")
	if index < 0 {
		return "", "", false
	}
	owner, relative = p[:index], p[index+len("/.resources/"):]
	if !safePath(owner, false) || relative == "" || strings.Contains(relative, "/.resources/") || strings.ContainsAny(relative, "\r\n\t") || !safePath(strings.ReplaceAll(relative, ":", "_"), false) {
		return "", "", false
	}
	return owner, relative, true
}
func hasLegacyResourceOwner(files map[string][]byte, owner string) bool {
	parts := strings.SplitN(path.Base(owner), "_", 2)
	if len(parts) != 2 || parts[1] == "" {
		return false
	}
	typ := strings.ToLower(strings.SplitN(parts[0], "-", 2)[0])
	if !validID(parts[0], typ) {
		return false
	}
	_, readme := files[owner+"/README.md"]
	_, progress := files[owner+"/progress.md"]
	return readme && progress
}
func legacyResourceDirectory(files map[string][]byte, dir string) bool {
	p := strings.TrimSuffix(dir, "/")
	owner, _, ok := legacyResourcePath(p + "/placeholder")
	return ok && hasLegacyResourceOwner(files, owner)
}

func legacyResourceReference(uri, id, owner string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Host != id || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(uri, "\x00\r\n\t") || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "", invalid("invalid legacy file resource reference")
	}
	// Decode once via url.Parse. Encoded path separators cannot acquire new
	// structure; a literal percent sequence in a filename remains literal data.
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return "", invalid("invalid legacy file resource reference")
	}
	p := owner + "/.resources" + u.Path
	actual, _, ok := legacyResourcePath(p)
	if !ok || actual != owner {
		return "", invalid("invalid legacy file resource reference")
	}
	return p, nil
}

// Construct a versioned descriptive map only for legacy input. Native v1
// intentionally keeps all checksummed provenance opaque, including these names.
func mapLegacyResources(s *contract.Snapshot, files map[string][]byte, byPath map[string]int, consumed map[string]bool) (bool, error) {
	owners := map[string]string{}
	for dir, i := range byPath {
		owners[s.Elements[i].ID] = dir
	}
	record := legacyResourceMap{Schema: resourceMapSchema, Entries: []legacyResourceEntry{}}
	emptyRecord, _ := json.Marshal(record)
	mappingBytes := int64(len(emptyRecord) + 1)
	digests := map[string]string{}
	for i := range s.Elements {
		e := &s.Elements[i]
		for ordinal := range e.ResourceRefs {
			ref := &e.ResourceRefs[ordinal]
			u, err := url.Parse(ref.URI)
			if err != nil || u.Scheme != "file" {
				continue
			}
			original := ref.URI
			p, err := legacyResourceReference(original, e.ID, owners[e.ID])
			if err != nil {
				return false, err
			}
			data, exists := files[p]
			if !exists {
				return false, invalid("legacy resource payload missing")
			}
			digest, cached := digests[p]
			if !cached {
				digest = digestBytes(data)
				digests[p] = digest
			}
			native := resourceURNPrefix + digest
			provenance := "provenance/resource-" + digest + ".bin"
			if !cached {
				if err = addProvenance(s, provenance, data); err != nil {
					return false, err
				}
			}
			entry := legacyResourceEntry{ElementID: e.ID, ResourceOrdinal: ordinal, OriginalURI: original, OriginalPath: p, NativeURN: native, ProvenancePath: provenance, SHA256: digest, Bytes: int64(len(data))}
			encoded, err := json.Marshal(entry)
			if err != nil {
				return false, invalid("invalid legacy resource mapping")
			}
			mappingBytes += int64(len(encoded))
			if len(record.Entries) > 0 {
				mappingBytes++ // comma between entries
			}
			if mappingBytes > MaxEntry {
				return false, invalid("legacy resource mapping size limit")
			}
			record.Entries = append(record.Entries, entry)
			ref.URI = native
			consumed[p] = true
		}
	}
	for p := range files {
		if strings.Contains(p, "/.resources/") && !consumed[p] {
			return false, invalid("orphan legacy resource payload")
		}
	}
	if len(record.Entries) == 0 {
		return false, nil
	}
	sort.Slice(record.Entries, func(i, j int) bool {
		a, b := record.Entries[i], record.Entries[j]
		if a.ElementID != b.ElementID {
			return a.ElementID < b.ElementID
		}
		return a.ResourceOrdinal < b.ResourceOrdinal
	})
	data, err := json.Marshal(record)
	if err != nil {
		return false, invalid("invalid legacy resource mapping")
	}
	data = append(data, '\n')
	if int64(len(data)) > MaxEntry {
		return false, invalid("legacy resource mapping size limit")
	}
	if err = addProvenance(s, resourceMapPath, data); err != nil {
		return false, err
	}
	return true, nil
}
