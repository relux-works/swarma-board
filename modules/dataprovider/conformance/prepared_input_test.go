package conformance

import (
	"context"
	"errors"
	"fmt"
	dp "github.com/relux-works/swarma-board/dataprovider"
	"testing"
)

type preparedFailure struct {
	err  error
	sent bool
}

func (r *preparedFailure) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "a"), nil
	}
	return 0, r.err
}

func TestPreparedInputFailureRollsBackWholeUpdate(t *testing.T) {
	for _, x := range []struct {
		name     string
		err      error
		code     dp.Code
		sentinel error
	}{
		{"ordinary", errors.New("private acquisition failure"), dp.StorageError, nil},
		{"typed-unknown", dp.NewError(dp.CommitOutcomeUnknown), dp.StorageError, nil},
		{"typed-not-found", dp.NewError(dp.NotFound), dp.StorageError, nil},
		{"wrapped-unknown", fmt.Errorf("private acquisition failure: %w", dp.NewError(dp.CommitOutcomeUnknown)), dp.StorageError, nil},
		{"wrapped-not-found", fmt.Errorf("private acquisition failure: %w", dp.NewError(dp.NotFound)), dp.StorageError, nil},
		{"canceled", context.Canceled, dp.Unavailable, context.Canceled},
		{"deadline", context.DeadlineExceeded, dp.Unavailable, context.DeadlineExceeded},
	} {
		t.Run(x.name, func(t *testing.T) {
			s := newSession(t, controlFactory(""), nil)
			s.setup()
			old := s.observe(s.scope())
			calls := 0
			c, err := s.p.Update(s.ctx, s.scope(), s.rev(1), s.actor, func(snap dp.Snapshot) (dp.ChangeSet, error) {
				calls++
				e := dp.CloneElement(snap.Elements[2])
				e.Notes = "must roll back"
				return dp.ChangeSet{Upserts: []dp.Element{e}, PutResources: []dp.ResourceInput{input("staged first"), {Info: dp.ResourceInfo{SHA256: abcDigest, Size: 3}, Reader: &preparedFailure{err: x.err}}}}, nil
			})
			equal(t, calls, 1)
			zeroCommit(t, c)
			code(t, err, x.code)
			if _, ok := err.(*dp.Error); !ok {
				t.Fatalf("non-closed error: %v", err)
			}
			if x.sentinel != nil && !errors.Is(err, x.sentinel) {
				t.Fatalf("sentinel lost: %v", err)
			}
			s.unchanged(old)
			s.reopen()
			s.unchanged(old)
			s.reopen()
			s.unchanged(old)
		})
	}
}
