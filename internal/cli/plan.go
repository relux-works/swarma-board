package cli

import "encoding/json"

// This is a wire view only. Graph construction, readiness, phases and critical
// paths remain server-owned and are never recomputed in the client.
type fetchedPlan struct {
	Usable             *bool             `json:"usable"`
	CriticalPath       []string          `json:"criticalPath"`
	Phases             []fetchedPhase    `json:"phases"`
	Scope              json.RawMessage   `json:"scope"`
	Summary            json.RawMessage   `json:"summary"`
	DependencyProblems []json.RawMessage `json:"dependency_problems"`
	TaskCycles         []json.RawMessage `json:"task_cycles"`
	ProjectionCycles   []json.RawMessage `json:"projection_cycles"`
}
type fetchedPhase struct {
	Phase      int               `json:"phase"`
	Elements   []json.RawMessage `json:"elements"`
	Containers []string          `json:"containers"`
}

func decodePlan(b json.RawMessage) (fetchedPlan, error) {
	var result struct {
		Plan *fetchedPlan `json:"plan"`
	}
	if json.Unmarshal(b, &result) != nil || result.Plan == nil {
		return fetchedPlan{}, local("server returned an invalid plan")
	}
	p := *result.Plan
	if p.Usable == nil || p.CriticalPath == nil || p.Phases == nil || p.Scope == nil || string(p.Scope) == "null" || p.Summary == nil || string(p.Summary) == "null" || p.DependencyProblems == nil || p.TaskCycles == nil || p.ProjectionCycles == nil {
		return p, local("server returned an incomplete plan")
	}
	for _, phase := range p.Phases {
		if phase.Phase < 1 || phase.Elements == nil || phase.Containers == nil {
			return p, local("server returned an invalid phase")
		}
	}
	return p, nil
}
