package domain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/relux-works/swarma-board/internal/contract"
)

// sourceMetadata matches the frozen transfer metadata fields; this avoids a
// dependency on a peer implementation. Only the exact digest-addressed record
// validated by Transfer is consumed. Historical provenance remains opaque.
type sourceMetadata struct {
	ArchiveSHA256   string            `json:"archive_sha256"`
	SourceFormat    string            `json:"source_format"`
	SourceVersion   int               `json:"source_version"`
	SourceProjectID string            `json:"source_project_id"`
	SourceRevision  contract.Revision `json:"source_revision"`
	SourceCommit    string            `json:"source_commit"`
	Exclusions      []string          `json:"exclusions"`
}

func (s *Service) Import(ctx context.Context, project string, expected contract.Revision, audit contract.AuditContext, prepared contract.PreparedImport) (contract.Commit, error) {
	zero := contract.Commit{}
	bad := func() (contract.Commit, error) { return zero, errCode("invalid_snapshot", "Invalid prepared import") }
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if strings.TrimSpace(audit.AccountID) == "" {
		return zero, errCode("unauthorized", "Authenticated actor required")
	}
	if !digestRE.MatchString(prepared.SHA256) || prepared.Report.SHA256 != prepared.SHA256 || prepared.Snapshot.Project.ID != project || prepared.Report.ElementCount != len(prepared.Snapshot.Elements) {
		return bad()
	}
	// Clone the candidate before entering the repository's transaction. A caller
	// retaining PreparedImport cannot change what this callback imports.
	data, err := json.Marshal(prepared.Snapshot)
	if err != nil {
		return bad()
	}
	var candidate contract.Snapshot
	if json.Unmarshal(data, &candidate) != nil {
		return bad()
	}
	if err = validateElements(candidate.Elements); err != nil {
		return bad()
	}
	transferPath := "provenance/transfer-" + prepared.SHA256 + ".json"
	importPath := "provenance/import-" + prepared.SHA256 + ".json"
	files := map[string]contract.ProvenanceFile{}
	var metadata sourceMetadata
	found := false
	for _, f := range candidate.Provenance {
		if len(f.Data) > 8<<20 || !strings.HasPrefix(f.Path, "provenance/") || f.Path == "provenance/" || path.Clean(f.Path) != f.Path || strings.ContainsAny(f.Path, "\\\x00") || !digestRE.MatchString(f.SHA256) {
			return bad()
		}
		sum := sha256.Sum256(f.Data)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return bad()
		}
		if old, ok := files[f.Path]; ok {
			if !bytes.Equal(old.Data, f.Data) {
				return bad()
			}
			continue
		}
		files[f.Path] = f
		if f.Path == transferPath {
			dec := json.NewDecoder(bytes.NewReader(f.Data))
			dec.DisallowUnknownFields()
			if dec.Decode(&metadata) != nil {
				return bad()
			}
			if dec.Decode(new(any)) != io.EOF {
				return bad()
			}
			found = true
		}
	}
	if !found || metadata.ArchiveSHA256 != prepared.SHA256 || metadata.SourceProjectID != candidate.Project.ID || metadata.SourceRevision != candidate.Revision || metadata.SourceFormat != prepared.Report.Format || metadata.SourceVersion != prepared.Report.Version {
		return bad()
	}
	if metadata.SourceFormat == "swarma-board" && metadata.SourceVersion != 1 || metadata.SourceFormat == "task-board" && metadata.SourceVersion != 2 || metadata.SourceFormat != "swarma-board" && metadata.SourceFormat != "task-board" {
		return bad()
	}
	exclusions := append([]string{}, prepared.Report.Exclusions...)
	sort.Strings(exclusions)
	metaEx := append([]string{}, metadata.Exclusions...)
	sort.Strings(metaEx)
	a, _ := json.Marshal(exclusions)
	b, _ := json.Marshal(metaEx)
	if !bytes.Equal(a, b) {
		return bad()
	}
	metadata.Exclusions = exclusions
	importData, err := json.Marshal(metadata)
	if err != nil {
		return bad()
	}
	if old, ok := files[importPath]; ok && !bytes.Equal(old.Data, importData) {
		return bad()
	}
	sum := sha256.Sum256(importData)
	files[importPath] = contract.ProvenanceFile{Path: importPath, SHA256: hex.EncodeToString(sum[:]), Data: importData}
	candidate.Provenance = []contract.ProvenanceFile{}
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		candidate.Provenance = append(candidate.Provenance, files[name])
	}
	return s.repo.Update(ctx, project, expected, audit, func(target contract.Snapshot) (contract.ChangeSet, error) {
		if err := ctx.Err(); err != nil {
			return contract.ChangeSet{}, err
		}
		if len(target.Elements) != 0 || len(target.Provenance) != 0 {
			return contract.ChangeSet{}, errCode("already_exists", "Import target must be empty")
		}
		return contract.ChangeSet{Upserts: candidate.Elements, Provenance: candidate.Provenance}, nil
	})
}
