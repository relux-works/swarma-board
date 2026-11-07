package conformance

import (
	"io"
	"testing"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

func TestCompleteHistoryFixtureUsesExactCanonicalBytes(t *testing.T) {
	v, err := Vectors()
	must(t, err)
	scope := dp.Scope{ProductID: "alpha", BoardID: "work"}
	a := v.Fixture.Actor
	a.Operation = "create"
	a.RequestID = "seed"
	seed := dp.Event{FormatVersion: 1, Scope: scope, Revision: dp.Revision{Scope: scope, Counter: 1}, Actor: a, ChangedIDs: []string{"EPIC-1", "STORY-1", "TASK-1"}, ChangedResourceIDs: []string{}}
	snap := dp.Snapshot{Board: dp.Board{Scope: scope}, Revision: seed.Revision}
	for _, target := range []int64{4096, 10000, 65536, 100000} {
		fixture, err := exactHistory(snap, seed, target)
		must(t, err)
		var total int64
		var events []dp.Event
		must(t, fixture.Emit(func(e dp.Event) error {
			events = append(events, e)
			n, err := dp.EncodeEvent(io.Discard, e)
			total += n + 1
			return err
		}))
		equal(t, total, target)
		equal(t, uint64(len(events)), fixture.Snapshot.Revision.Counter)
		must(t, dp.ValidateEvents(fixture.Snapshot, events))
	}
}
