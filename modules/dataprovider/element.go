package dataprovider

// These native-v1 record declarations are copied without field/tag/nullability
// changes from internal/contract/element.go at f403f0ed5042e79e1b718a98bec3b839eba4b78a.
// That explicit serialization boundary keeps the standalone module independent
// of root internal packages. The shared integrator must alias or convert every
// field at its boundary; domain validation/derived edges remain in the root.

import "time"

// Element is the native v1 stored record. Selectors and wire result adapters
// belong to their owning packages; this record contains no derived authority.
type Element struct {
	ID                 string          `json:"id"`
	Type               string          `json:"type"`
	Name               string          `json:"name"`
	ParentID           string          `json:"parent_id"`
	Title              string          `json:"title"`
	Description        string          `json:"description"`
	Scope              string          `json:"scope"`
	AcceptanceCriteria string          `json:"acceptance_criteria"`
	Status             string          `json:"status"`
	AssignedTo         string          `json:"assigned_to"`
	CreatedAt          *time.Time      `json:"created_at"`
	UpdatedAt          *time.Time      `json:"updated_at"`
	BlockedBy          []string        `json:"blocked_by"`
	ManualBlockedBy    []string        `json:"manual_blocked_by"`
	Checklist          []ChecklistItem `json:"checklist"`
	Notes              string          `json:"notes"`
	Estimate           *Estimate       `json:"estimate"`
	Review             string          `json:"review,omitempty"`
	TaskClass          string          `json:"task_class,omitempty"`
	ResourceRefs       []ResourceRef   `json:"resource_refs"`
}

type Estimate struct {
	Kind  string         `json:"kind"`
	Scale *EstimateScale `json:"scale,omitempty"`
}

type EstimateScale struct {
	Kind   string `json:"kind"`
	Number int    `json:"number"`
}

type ChecklistItem struct {
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
}

type ResourceRef struct {
	Name        string `json:"name"`
	URI         string `json:"uri"`
	Description string `json:"description"`
	Type        string `json:"type"`
}
