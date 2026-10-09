package dotdurable

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
)

type DecideActivity struct {
	Snapshots SnapshotLoader
	Reasoner  Reasoner
	Store     Store
}

func (a DecideActivity) Execute(ctx context.Context, req Request) (Record, error) {
	if a.Snapshots == nil || a.Reasoner == nil || a.Store == nil {
		return Record{}, errors.New("durable Dot decision activity is not fully configured")
	}
	if err := req.Validate(); err != nil {
		return Record{}, nonRetryable("INVALID_DOT_DECISION_REQUEST", err)
	}

	existing, err := a.Store.GetCommittedDecision(ctx, req.DecisionID)
	switch {
	case err == nil:
		if existing.SnapshotDigest != req.SnapshotDigest {
			return Record{}, nonRetryable("DOT_DECISION_CONFLICT", ErrDecisionConflict)
		}
		return existing, nil
	case !errors.Is(err, ErrDecisionNotFound):
		return Record{}, fmt.Errorf("load committed Dot decision: %w", err)
	}

	snapshot, err := a.Snapshots.LoadSnapshot(ctx, req.SnapshotDigest)
	if err != nil {
		return Record{}, fmt.Errorf("load sealed Portfolio Context: %w", err)
	}
	if snapshot.SnapshotDigest != req.SnapshotDigest {
		return Record{}, nonRetryable("DOT_SNAPSHOT_MISMATCH", ErrSnapshotMismatch)
	}

	decision, err := a.Reasoner.Decide(ctx, snapshot.ReasoningView())
	if err != nil {
		return Record{}, fmt.Errorf("Dot reasoning: %w", err)
	}
	if decision.SnapshotDigest != req.SnapshotDigest {
		return Record{}, nonRetryable("DOT_SNAPSHOT_MISMATCH", ErrSnapshotMismatch)
	}
	if err := dotdecision.Validate(snapshot, decision); err != nil {
		return Record{}, nonRetryable("INVALID_DOT_DECISION", err)
	}

	digest, err := DecisionDigest(decision)
	if err != nil {
		return Record{}, nonRetryable("INVALID_DOT_DECISION", err)
	}
	record := Record{
		DecisionID:         req.DecisionID,
		SnapshotDigest:     decision.SnapshotDigest,
		WorkItemID:         decision.WorkItemID,
		Kind:               decision.Kind,
		RequestedAuthority: decision.RequestedAuthority,
		RequestedAction:    decision.RequestedAction,
		Rationale:          decision.Rationale,
		DecisionDigest:     digest,
	}
	if err := record.Validate(); err != nil {
		return Record{}, nonRetryable("INVALID_DOT_DECISION_RECORD", err)
	}

	stored, _, err := a.Store.PutCommittedDecision(ctx, record)
	if err != nil {
		if errors.Is(err, ErrDecisionConflict) {
			return Record{}, nonRetryable("DOT_DECISION_CONFLICT", err)
		}
		return Record{}, fmt.Errorf("commit Dot decision: %w", err)
	}
	if !SameRecord(stored, record) {
		return Record{}, nonRetryable("DOT_DECISION_CONFLICT", ErrDecisionConflict)
	}
	return stored, nil
}

func nonRetryable(kind string, err error) error {
	return temporal.NewNonRetryableApplicationError(err.Error(), kind, err)
}
