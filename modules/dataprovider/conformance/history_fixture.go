package conformance

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

// exactHistory builds a complete, streamable, resource-free synthetic history
// of exactly target canonical NDJSON bytes, within the actual common cap.
// It varies only admitted synthetic Actor lengths; no event is omitted.
func exactHistory(snapshot dp.Snapshot, seed dp.Event, target int64) (HistoryFixture, error) {
	if len(snapshot.Resources) != 0 || target > dp.MaxEventNDJSONBytes {
		return HistoryFixture{}, fmt.Errorf("invalid resource-free history fixture target")
	}
	seedBytes, err := dp.EncodeEvent(io.Discard, seed)
	if err != nil {
		return HistoryFixture{}, err
	}
	seedBytes++
	baseActor := seed.Actor
	baseActor.AccountID = "e"
	baseActor.RequestID = "r"
	baseActor.Operation = "link"
	makeEvent := func(counter uint64, extra int) dp.Event {
		a := baseActor
		a.AccountID += strings.Repeat("x", min(extra, 255))
		a.RequestID += strings.Repeat("r", max(0, extra-255))
		return dp.Event{FormatVersion: 1, Scope: snapshot.Board.Scope, Revision: dp.Revision{Scope: snapshot.Board.Scope, Counter: counter}, Actor: a, ChangedIDs: []string{}, ChangedResourceIDs: []string{}}
	}
	baseBytes, err := dp.EncodeEvent(io.Discard, makeEvent(1, 0))
	if err != nil {
		return HistoryFixture{}, err
	}
	baseBytes++
	minBytes := func(counter uint64) int64 { return baseBytes + int64(len(strconv.FormatUint(counter, 10))-1) }
	remaining := target - seedBytes
	if remaining < 0 {
		return HistoryFixture{}, fmt.Errorf("history target smaller than seed")
	}
	// A maximal actor adds 255 account and 127 request bytes without escaping.
	const actorPadding = 382
	next := uint64(2)
	fullUntil := uint64(1)
	for remaining > 2*(minBytes(next)+actorPadding) {
		remaining -= minBytes(next) + actorPadding
		fullUntil = next
		next++
	}
	type tailEvent struct {
		counter uint64
		extra   int
	}
	tail := []tailEvent{}
	if remaining > 0 {
		minimum := minBytes(next)
		maximum := minimum + actorPadding
		if remaining >= minimum && remaining <= maximum {
			tail = append(tail, tailEvent{next, int(remaining - minimum)})
			next++
		} else {
			secondMin := minBytes(next + 1)
			secondMax := secondMin + actorPadding
			first := max(minimum, remaining-secondMax)
			if first > maximum || remaining-first < secondMin || remaining-first > secondMax {
				return HistoryFixture{}, fmt.Errorf("history target cannot be represented by complete admitted events")
			}
			tail = append(tail, tailEvent{next, int(first - minimum)}, tailEvent{next + 1, int(remaining - first - secondMin)})
			next += 2
		}
	}
	snapshot = dp.CloneSnapshot(snapshot)
	snapshot.Revision.Counter = next - 1
	return HistoryFixture{Snapshot: snapshot, NDJSONBytes: target, Emit: func(yield func(dp.Event) error) error {
		if err := yield(seed); err != nil {
			return err
		}
		for n := uint64(2); n <= fullUntil; n++ {
			if err := yield(makeEvent(n, actorPadding)); err != nil {
				return err
			}
		}
		for _, e := range tail {
			if err := yield(makeEvent(e.counter, e.extra)); err != nil {
				return err
			}
		}
		return nil
	}}, nil
}

func minimumNextEvent(s dp.Snapshot, actor dp.Actor) dp.Event {
	actor.AccountID = "e"
	actor.RequestID = "r"
	actor.Operation = "link"
	return dp.Event{FormatVersion: 1, Scope: s.Board.Scope, Revision: dp.Revision{Scope: s.Board.Scope, Counter: s.Revision.Counter + 1}, Actor: actor, ChangedIDs: []string{}, ChangedResourceIDs: []string{}}
}
