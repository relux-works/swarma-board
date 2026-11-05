// Package domain owns task facts, transaction-scoped changes and pure planning.
// Product rules are selectively adapted from skill-project-management at
// 09bac3d89822c9b83c04dff6483d7b2074097a82, pkg/board/{estimate,validation,
// dependencies,dependency_cycles}.go and plan/graph.go. Runtime/checkpoint
// authority and legacy execution orchestration are intentionally absent.
package domain

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"strings"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/query"
)

type Service struct {
	repo contract.Repository
	now  func() time.Time
	id   func(string) (string, error)
}

var _ contract.Service = (*Service)(nil)

func New(repo contract.Repository, now func() time.Time, newID func(string) (string, error)) (*Service, error) {
	if repo == nil || now == nil || newID == nil {
		return nil, errCode("unavailable", "Domain inputs required")
	}
	return &Service{repo, now, newID}, nil
}

// RandomID can be composed with the injected clock. It uses uniformly sampled
// six-character base36 suffixes and never opens a board or discovers credentials.
func RandomID(elementType string, at time.Time) (string, error) {
	if !query.Contains(query.Types, elementType) {
		return "", errCode("query_error", "Invalid element type")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(2176782336))
	if err != nil {
		return "", errCode("unavailable", "ID generation failed")
	}
	suffix := n.Text(36)
	suffix = strings.Repeat("0", 6-len(suffix)) + suffix
	return strings.ToUpper(elementType) + "-" + at.UTC().Format("060102") + "-" + suffix, nil
}
func (s *Service) Snapshot(ctx context.Context, project string) (contract.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return contract.Snapshot{}, err
	}
	return s.repo.View(ctx, project)
}
func (s *Service) Execute(ctx context.Context, call contract.Call) (contract.Reply, error) {
	op, err := query.Validate(call.Operation)
	if err != nil {
		return contract.Reply{}, err
	}
	if err = ctx.Err(); err != nil {
		return contract.Reply{}, err
	}
	if query.IsRead(op.Name) {
		snap, err := s.repo.View(ctx, call.ProjectID)
		if err != nil {
			return contract.Reply{}, err
		}
		payload, err := read(snap, op)
		if err != nil {
			return contract.Reply{}, err
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return contract.Reply{}, errCode("unavailable", "Result encoding failed")
		}
		return contract.Reply{Revision: snap.Revision, JSON: data}, nil
	}
	if strings.TrimSpace(call.Audit.AccountID) == "" {
		return contract.Reply{}, errCode("unauthorized", "Authenticated actor required")
	}
	if call.Expected == nil {
		return contract.Reply{}, errCode("revision_required", "Expected revision required")
	}
	var payload any
	commit, err := s.repo.Update(ctx, call.ProjectID, *call.Expected, call.Audit, func(snapshot contract.Snapshot) (contract.ChangeSet, error) {
		if err := ctx.Err(); err != nil {
			return contract.ChangeSet{}, err
		}
		changes, id, err := s.mutate(snapshot, op)
		if err != nil {
			return contract.ChangeSet{}, err
		}
		changed := []string{}
		for _, e := range changes.Upserts {
			changed = append(changed, e.ID)
		}
		changed = append(changed, changes.Deletes...)
		changed = query.Unique(changed)
		payload = map[string]any{op.Name: map[string]any{"id": id, "changed_ids": changed}}
		return changes, nil
	})
	if err != nil {
		return contract.Reply{}, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return contract.Reply{}, errCode("unavailable", "Result encoding failed")
	}
	return contract.Reply{Revision: commit.Revision, JSON: data}, nil
}
func read(snapshot contract.Snapshot, op contract.Operation) (any, error) {
	b := query.Index(snapshot.Elements)
	switch op.Name {
	case "get":
		e, ok := b.Find(query.String(op, "id"))
		if !ok {
			return nil, errCode("not_found", "Element not found")
		}
		return map[string]any{"get": b.Select(e, op.Fields, false)}, nil
	case "plan":
		p, err := BuildPlan(snapshot, op)
		if err != nil {
			return nil, err
		}
		return map[string]any{"plan": p}, nil
	case "summary":
		types := map[string]int{}
		statuses := map[string]int{}
		for _, t := range query.Types {
			types[t] = 0
		}
		for _, t := range query.Statuses {
			statuses[t] = 0
		}
		for _, e := range snapshot.Elements {
			types[e.Type]++
			statuses[e.Status]++
		}
		return map[string]any{"summary": map[string]any{"total": len(snapshot.Elements), "byType": types, "byStatus": statuses}}, nil
	case "list", "count":
		rows := []map[string]any{}
		count := 0
		for _, id := range b.IDs {
			e := b.Elements[id]
			if !matches(b, e, op) {
				continue
			}
			if op.Name == "list" && count >= query.Int(op, "skip") && len(rows) < query.Int(op, "take") {
				rows = append(rows, b.Select(e, op.Fields, false))
			}
			count++
		}
		if op.Name == "count" {
			return map[string]any{"count": count}, nil
		}
		return map[string]any{"list": rows}, nil
	}
	return nil, errCode("unsupported_operation", "Unsupported read")
}
func matches(b *query.Board, e contract.Element, op contract.Operation) bool {
	for key, raw := range op.Args {
		switch key {
		case "type":
			if e.Type != query.String(op, key) {
				return false
			}
		case "status":
			if e.Status != query.String(op, key) {
				return false
			}
		case "parent":
			if !strings.EqualFold(e.ParentID, query.String(op, key)) {
				return false
			}
		case "assignee":
			if e.AssignedTo != query.String(op, key) {
				return false
			}
		case "blocked":
			var v bool
			_ = json.Unmarshal(raw, &v)
			if (b.Blocked(e) || b.Advisory(e)) != v {
				return false
			}
		}
	}
	return true
}

func (s *Service) mutate(snapshot contract.Snapshot, op contract.Operation) (contract.ChangeSet, string, error) {
	empty := contract.ChangeSet{}
	b := query.Index(snapshot.Elements)
	id := query.String(op, "id")
	e, found := b.Find(id)
	id = e.ID
	now := s.now().UTC()
	if op.Name != "create" && !found {
		return empty, "", errCode("not_found", "Element not found")
	}
	if op.Name == "create" {
		if len(snapshot.Elements) >= 10000 {
			return empty, "", errCode("invalid_snapshot", "Element limit exceeded")
		}
		typ := query.String(op, "type")
		id = ""
		for i := 0; i < 128; i++ {
			candidate, err := s.id(typ)
			if err != nil {
				return empty, "", err
			}
			if !newID.MatchString(candidate) || !strings.HasPrefix(candidate, strings.ToUpper(typ)+"-") {
				return empty, "", errCode("unavailable", "ID generator returned invalid candidate")
			}
			if _, ok := b.Find(candidate); !ok {
				id = candidate
				break
			}
		}
		if id == "" {
			return empty, "", errCode("unavailable", "ID collision retry limit")
		}
		name := normalizeName(query.String(op, "name"))
		if name == "" {
			return empty, "", errCode("query_error", "Name requires a nonempty slug")
		}
		e = contract.Element{ID: id, Type: typ, Name: name, Title: name, ParentID: query.String(op, "parent"), Description: query.String(op, "description"), Scope: query.String(op, "scope"), AcceptanceCriteria: query.String(op, "ac"), Status: "backlog", Review: "required", TaskClass: "code", CreatedAt: &now, UpdatedAt: &now, BlockedBy: []string{}, ManualBlockedBy: []string{}, Checklist: []contract.ChecklistItem{}, ResourceRefs: []contract.ResourceRef{}}
		if query.Leaf(typ) {
			e.Estimate = &contract.Estimate{Kind: "notEstimated"}
		}
		if v := query.String(op, "review"); v != "" {
			e.Review = v
		}
		if v := query.String(op, "task_class"); v != "" {
			e.TaskClass = v
		}
	} else {
		// Detach variable-length fields before modifying a repository snapshot.
		e.BlockedBy = append([]string{}, e.BlockedBy...)
		e.ManualBlockedBy = append([]string{}, e.ManualBlockedBy...)
		e.Checklist = append([]contract.ChecklistItem{}, e.Checklist...)
		switch op.Name {
		case "set_details":
			for key := range op.Args {
				v := query.String(op, key)
				switch key {
				case "title":
					e.Title = v
				case "description":
					e.Description = v
				case "scope":
					e.Scope = v
				case "ac":
					e.AcceptanceCriteria = v
				case "review":
					e.Review = v
				case "task_class":
					e.TaskClass = v
				}
			}
		case "rename":
			e.Name = normalizeName(query.String(op, "name"))
			if e.Name == "" {
				return empty, "", errCode("query_error", "Name requires a nonempty slug")
			}
		case "assign":
			e.AssignedTo = query.String(op, "agent")
		case "unassign":
			e.AssignedTo = ""
		case "set_status":
			status := query.String(op, "status")
			if status == "development" && e.Status != "development" && query.Leaf(e.Type) && e.Estimate != nil && e.Estimate.Kind != "estimated" {
				return empty, "", errCode("estimate_required", "Explicit estimate required before development")
			}
			e.Status = status
		case "set_estimate":
			if !query.Leaf(e.Type) {
				return empty, "", errCode("query_error", "Only leaves have writable estimates")
			}
			e.Estimate = &contract.Estimate{Kind: query.String(op, "estimate")}
			if e.Estimate.Kind == "estimated" {
				e.Estimate.Scale = &contract.EstimateScale{Kind: "fibonacci", Number: query.Int(op, "number")}
			}
		case "link", "unlink":
			if !query.Leaf(e.Type) {
				return empty, "", errCode("dependency_conflict", "Task links require leaves")
			}
			target, ok := b.Find(query.String(op, "blocked_by"))
			if !ok {
				return empty, "", errCode("not_found", "Blocker not found")
			}
			if !query.Leaf(target.Type) {
				return empty, "", errCode("dependency_conflict", "Task links require leaves")
			}
			edges := []string{}
			for _, v := range e.BlockedBy {
				if !strings.EqualFold(v, target.ID) {
					edges = append(edges, v)
				}
			}
			if op.Name == "link" {
				edges = append(edges, target.ID)
			}
			e.BlockedBy = query.Unique(edges)
		case "check_item", "uncheck_item", "remove_checklist_item":
			n := query.Int(op, "item") - 1
			if n < 0 || n >= len(e.Checklist) {
				return empty, "", errCode("query_error", "Checklist index out of range")
			}
			if op.Name == "remove_checklist_item" {
				e.Checklist = append(e.Checklist[:n], e.Checklist[n+1:]...)
			} else {
				e.Checklist[n].Checked = op.Name == "check_item"
			}
		case "add_checklist_item":
			e.Checklist = append(e.Checklist, contract.ChecklistItem{Text: query.String(op, "text")})
		case "set_notes":
			text := query.String(op, "text")
			if query.Bool(op, "set") || e.Notes == "" {
				e.Notes = text
			} else {
				e.Notes += "\n" + text
			}
		case "set_parent":
			e.ParentID = query.String(op, "parent")
		case "delete":
			if !query.Leaf(e.Type) || len(b.Children[e.ID]) > 0 {
				return empty, "", errCode("dependency_conflict", "Delete requires a leaf without children")
			}
			for _, other := range snapshot.Elements {
				for _, ref := range append(append([]string{}, other.BlockedBy...), other.ManualBlockedBy...) {
					if strings.EqualFold(ref, e.ID) {
						return empty, "", errCode("dependency_conflict", "Unlink incoming references before deletion")
					}
				}
			}
			return contract.ChangeSet{Deletes: []string{e.ID}}, e.ID, nil
		default:
			return empty, "", errCode("unsupported_operation", "Unsupported mutation")
		}
		e.UpdatedAt = &now
	}
	if e.ParentID != "" {
		if p, ok := b.Find(e.ParentID); ok {
			e.ParentID = p.ID
		}
	}
	// All graph and lifecycle invariants are checked against this callback's
	// supplied snapshot, never against a prior repository View.
	candidate := append([]contract.Element{}, snapshot.Elements...)
	replaced := false
	for i, v := range candidate {
		if v.ID == e.ID {
			candidate[i] = e
			replaced = true
		}
	}
	if !replaced {
		candidate = append(candidate, e)
	}
	if err := validateElements(candidate); err != nil {
		return empty, "", err
	}
	return contract.ChangeSet{Upserts: []contract.Element{e}}, e.ID, nil
}
