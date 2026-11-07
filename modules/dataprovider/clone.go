package dataprovider

import "slices"

// CloneElement preserves every native-v1 null/empty and nested order distinction.
func CloneElement(e Element) Element {
	e.BlockedBy = slices.Clone(e.BlockedBy)
	e.ManualBlockedBy = slices.Clone(e.ManualBlockedBy)
	e.Checklist = slices.Clone(e.Checklist)
	e.ResourceRefs = slices.Clone(e.ResourceRefs)
	if e.CreatedAt != nil {
		v := *e.CreatedAt
		e.CreatedAt = &v
	}
	if e.UpdatedAt != nil {
		v := *e.UpdatedAt
		e.UpdatedAt = &v
	}
	if e.Estimate != nil {
		v := *e.Estimate
		e.Estimate = &v
		if v.Scale != nil {
			x := *v.Scale
			e.Estimate.Scale = &x
		}
	}
	return e
}
func cloneElements(in []Element) []Element {
	out := slices.Clone(in)
	for i := range out {
		out[i] = CloneElement(out[i])
	}
	return out
}
func cloneProvenance(in []ProvenanceFile) []ProvenanceFile {
	out := slices.Clone(in)
	for i := range out {
		out[i].Data = slices.Clone(out[i].Data)
	}
	return out
}
func CloneSnapshot(s Snapshot) Snapshot {
	s.Elements = cloneElements(s.Elements)
	s.Provenance = cloneProvenance(s.Provenance)
	s.Resources = slices.Clone(s.Resources)
	return s
}

// CloneChangeSet copies metadata only. Input reader ownership is unchanged; a
// provider must consume readers once into private staging and retain none after
// Update returns. This function neither reads nor closes them.
func CloneChangeSet(c ChangeSet) ChangeSet {
	c.Upserts = cloneElements(c.Upserts)
	c.Deletes = slices.Clone(c.Deletes)
	c.Provenance = cloneProvenance(c.Provenance)
	c.PutResources = slices.Clone(c.PutResources)
	c.DeleteResources = slices.Clone(c.DeleteResources)
	return c
}
func CloneEvents(events []Event) []Event {
	out := slices.Clone(events)
	for i := range out {
		out[i].ChangedIDs = slices.Clone(out[i].ChangedIDs)
		out[i].ChangedResourceIDs = slices.Clone(out[i].ChangedResourceIDs)
	}
	return out
}
