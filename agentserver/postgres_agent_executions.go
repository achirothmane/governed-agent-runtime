package agentserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporalagent"
)

var (
	_ temporalagent.ExecutionStore = (*PostgresStore)(nil)
	_ temporalagent.StepStore      = (*PostgresStore)(nil)
)

func (s *PostgresStore) PrepareExecution(ctx context.Context, ref temporalagent.ExecutionRef) (temporalagent.ExecutionRef, bool, error) {
	if s == nil || s.db == nil {
		return temporalagent.ExecutionRef{}, false, errors.New("postgres store is not configured")
	}
	if err := ref.Validate(); err != nil {
		return temporalagent.ExecutionRef{}, false, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_server_agent_executions (
			run_id, conversation_id, run_fingerprint, mission_digest, max_steps, created_at
		)
		VALUES ($1,$2,$3,$4,$5,NOW())
		ON CONFLICT (run_id) DO NOTHING
		RETURNING run_id, conversation_id, run_fingerprint, mission_digest, max_steps
	`,
		string(ref.RunID),
		string(ref.ConversationID),
		ref.RunFingerprint,
		ref.MissionDigest,
		ref.MaxSteps,
	)
	stored, err := scanAgentExecution(row)
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return temporalagent.ExecutionRef{}, false, fmt.Errorf("insert agent execution: %w", err)
	}
	stored, err = s.GetExecution(ctx, ref.RunID)
	if err != nil {
		return temporalagent.ExecutionRef{}, false, err
	}
	if stored != ref {
		return temporalagent.ExecutionRef{}, false, temporalagent.ErrExecutionConflict
	}
	return stored, false, nil
}

func (s *PostgresStore) GetExecution(ctx context.Context, runID runtimesdk.RunID) (temporalagent.ExecutionRef, error) {
	if s == nil || s.db == nil {
		return temporalagent.ExecutionRef{}, errors.New("postgres store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, conversation_id, run_fingerprint, mission_digest, max_steps
		FROM agent_server_agent_executions
		WHERE run_id = $1
	`, string(runID))
	ref, err := scanAgentExecution(row)
	if errors.Is(err, sql.ErrNoRows) {
		return temporalagent.ExecutionRef{}, fmt.Errorf("%w: %s", temporalagent.ErrExecutionNotFound, runID)
	}
	if err != nil {
		return temporalagent.ExecutionRef{}, fmt.Errorf("get agent execution: %w", err)
	}
	return ref, nil
}

func (s *PostgresStore) PutDecision(ctx context.Context, record temporalagent.DecisionRecord) (temporalagent.DecisionRecord, bool, error) {
	if s == nil || s.db == nil {
		return temporalagent.DecisionRecord{}, false, errors.New("postgres store is not configured")
	}
	if err := record.Validate(); err != nil {
		return temporalagent.DecisionRecord{}, false, err
	}
	payload, err := json.Marshal(record.Decision)
	if err != nil {
		return temporalagent.DecisionRecord{}, false, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_server_reasoning_steps (
			run_id, step, decision_json, decision_digest, created_at
		)
		VALUES ($1,$2,$3,$4,NOW())
		ON CONFLICT (run_id, step) DO NOTHING
		RETURNING run_id, step, decision_json, decision_digest
	`,
		string(record.RunID),
		record.Step,
		payload,
		record.DecisionDigest,
	)
	stored, err := scanDecision(row)
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return temporalagent.DecisionRecord{}, false, fmt.Errorf("insert reasoning decision: %w", err)
	}
	stored, err = s.GetDecision(ctx, record.RunID, record.Step)
	if err != nil {
		return temporalagent.DecisionRecord{}, false, err
	}
	if stored.DecisionDigest != record.DecisionDigest {
		return temporalagent.DecisionRecord{}, false, temporalagent.ErrDecisionConflict
	}
	return stored, false, nil
}

func (s *PostgresStore) GetDecision(ctx context.Context, runID runtimesdk.RunID, step int) (temporalagent.DecisionRecord, error) {
	if s == nil || s.db == nil {
		return temporalagent.DecisionRecord{}, errors.New("postgres store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, step, decision_json, decision_digest
		FROM agent_server_reasoning_steps
		WHERE run_id = $1 AND step = $2
	`, string(runID), step)
	record, err := scanDecision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return temporalagent.DecisionRecord{}, fmt.Errorf("%w: run=%s step=%d", temporalagent.ErrDecisionNotFound, runID, step)
	}
	if err != nil {
		return temporalagent.DecisionRecord{}, fmt.Errorf("get reasoning decision: %w", err)
	}
	return record, nil
}

func scanAgentExecution(scanner rowScanner) (temporalagent.ExecutionRef, error) {
	var (
		runID          string
		conversationID string
		fingerprint    string
		missionDigest  string
		maxSteps       int
	)
	if err := scanner.Scan(&runID, &conversationID, &fingerprint, &missionDigest, &maxSteps); err != nil {
		return temporalagent.ExecutionRef{}, err
	}
	ref := temporalagent.ExecutionRef{
		RunID:          runtimesdk.RunID(runID),
		ConversationID: runtimesdk.ConversationID(conversationID),
		RunFingerprint: fingerprint,
		MissionDigest:  missionDigest,
		MaxSteps:       maxSteps,
	}
	if err := ref.Validate(); err != nil {
		return temporalagent.ExecutionRef{}, fmt.Errorf("postgres contains invalid agent execution: %w", err)
	}
	return ref, nil
}

func scanDecision(scanner rowScanner) (temporalagent.DecisionRecord, error) {
	var (
		runID    string
		step     int
		payload  []byte
		digest   string
	)
	if err := scanner.Scan(&runID, &step, &payload, &digest); err != nil {
		return temporalagent.DecisionRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var decision agentloop.Decision
	if err := decoder.Decode(&decision); err != nil {
		return temporalagent.DecisionRecord{}, fmt.Errorf("decode reasoning decision: %w", err)
	}
	record := temporalagent.DecisionRecord{
		RunID:          runtimesdk.RunID(runID),
		Step:           step,
		Decision:       decision,
		DecisionDigest: digest,
	}
	if err := record.Validate(); err != nil {
		return temporalagent.DecisionRecord{}, fmt.Errorf("postgres contains invalid reasoning decision: %w", err)
	}
	return record, nil
}

