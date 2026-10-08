package agentserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/dotdurable"
)

var _ dotdurable.Store = (*PostgresStore)(nil)

func (s *PostgresStore) PutCommittedDecision(ctx context.Context, record dotdurable.Record) (dotdurable.Record, bool, error) {
	if s == nil || s.db == nil {
		return dotdurable.Record{}, false, errors.New("postgres store is not configured")
	}
	if err := record.Validate(); err != nil {
		return dotdurable.Record{}, false, err
	}

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO portfolio_dot_decisions (
			decision_id, snapshot_digest, work_item_id, decision_kind,
			requested_authority, requested_action, rationale, decision_digest, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NOW())
		ON CONFLICT (decision_id) DO NOTHING
		RETURNING decision_id, snapshot_digest, work_item_id, decision_kind,
			requested_authority, requested_action, rationale, decision_digest
	`,
		record.DecisionID,
		record.SnapshotDigest,
		record.WorkItemID,
		string(record.Kind),
		record.RequestedAuthority,
		nullString(record.RequestedAction),
		record.Rationale,
		record.DecisionDigest,
	)
	stored, err := scanDotDecision(row)
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return dotdurable.Record{}, false, fmt.Errorf("insert durable Dot decision: %w", err)
	}

	stored, err = s.GetCommittedDecision(ctx, record.DecisionID)
	if err != nil {
		return dotdurable.Record{}, false, err
	}
	if !dotdurable.SameRecord(stored, record) {
		return dotdurable.Record{}, false, dotdurable.ErrDecisionConflict
	}
	return stored, false, nil
}

func (s *PostgresStore) GetCommittedDecision(ctx context.Context, decisionID string) (dotdurable.Record, error) {
	if s == nil || s.db == nil {
		return dotdurable.Record{}, errors.New("postgres store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT decision_id, snapshot_digest, work_item_id, decision_kind,
			requested_authority, requested_action, rationale, decision_digest
		FROM portfolio_dot_decisions
		WHERE decision_id = $1
	`, decisionID)
	record, err := scanDotDecision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return dotdurable.Record{}, fmt.Errorf("%w: %s", dotdurable.ErrDecisionNotFound, decisionID)
	}
	if err != nil {
		return dotdurable.Record{}, fmt.Errorf("get durable Dot decision: %w", err)
	}
	return record, nil
}

func scanDotDecision(scanner rowScanner) (dotdurable.Record, error) {
	var (
		decisionID string
		snapshotDigest string
		workItemID string
		kind string
		requestedAuthority string
		requestedAction sql.NullString
		rationale string
		decisionDigest string
	)
	if err := scanner.Scan(
		&decisionID, &snapshotDigest, &workItemID, &kind,
		&requestedAuthority, &requestedAction, &rationale, &decisionDigest,
	); err != nil {
		return dotdurable.Record{}, err
	}
	record := dotdurable.Record{
		DecisionID: decisionID,
		SnapshotDigest: snapshotDigest,
		WorkItemID: workItemID,
		Kind: dotdecision.Kind(kind),
		RequestedAuthority: requestedAuthority,
		Rationale: rationale,
		DecisionDigest: decisionDigest,
	}
	if requestedAction.Valid {
		record.RequestedAction = requestedAction.String
	}
	if err := record.Validate(); err != nil {
		return dotdurable.Record{}, fmt.Errorf("postgres contains invalid durable Dot decision: %w", err)
	}
	return record, nil
}
