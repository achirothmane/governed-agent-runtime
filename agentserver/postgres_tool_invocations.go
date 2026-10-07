package agentserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

var _ temporaltools.InvocationStore = (*PostgresStore)(nil)

func (s *PostgresStore) Prepare(ctx context.Context, ref temporaltools.InvocationRef, arguments map[string]any) (temporaltools.Invocation, bool, error) {
	if s == nil || s.db == nil {
		return temporaltools.Invocation{}, false, errors.New("postgres store is not configured")
	}
	if err := ref.Validate(); err != nil {
		return temporaltools.Invocation{}, false, err
	}
	digest, err := temporaltools.ArgumentsDigest(arguments)
	if err != nil {
		return temporaltools.Invocation{}, false, err
	}
	if digest != ref.ArgumentsDigest {
		return temporaltools.Invocation{}, false, temporaltools.ErrInvocationConflict
	}
	payload, err := json.Marshal(arguments)
	if err != nil {
		return temporaltools.Invocation{}, false, err
	}

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_server_tool_invocations (
			invocation_id, run_id, conversation_id,
			tool_name, tool_protocol, tool_endpoint, tool_read_only, snapshot_digest,
			arguments_json, arguments_digest, state, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'PREPARED',NOW(),NOW())
		ON CONFLICT (invocation_id) DO NOTHING
		RETURNING `+postgresInvocationColumns,
		ref.InvocationID,
		string(ref.RunID),
		string(ref.ConversationID),
		string(ref.Tool.Name),
		string(ref.Tool.Protocol),
		ref.Tool.Endpoint,
		ref.Tool.ReadOnly,
		ref.Tool.SnapshotDigest,
		payload,
		ref.ArgumentsDigest,
	)
	stored, scanErr := scanInvocation(row)
	if scanErr == nil {
		return stored, true, nil
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return temporaltools.Invocation{}, false, fmt.Errorf("insert tool invocation: %w", scanErr)
	}
	stored, err = s.Get(ctx, ref.InvocationID)
	if err != nil {
		return temporaltools.Invocation{}, false, err
	}
	if !temporaltools.SameBinding(stored.Ref, ref) {
		return temporaltools.Invocation{}, false, temporaltools.ErrInvocationConflict
	}
	return stored, false, nil
}

func (s *PostgresStore) Get(ctx context.Context, invocationID string) (temporaltools.Invocation, error) {
	if s == nil || s.db == nil {
		return temporaltools.Invocation{}, errors.New("postgres store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT `+postgresInvocationColumns+`
		FROM agent_server_tool_invocations
		WHERE invocation_id = $1`,
		invocationID,
	)
	invocation, err := scanInvocation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return temporaltools.Invocation{}, fmt.Errorf("%w: %s", temporaltools.ErrInvocationNotFound, invocationID)
	}
	if err != nil {
		return temporaltools.Invocation{}, fmt.Errorf("get tool invocation: %w", err)
	}
	return invocation, nil
}

func (s *PostgresStore) Complete(ctx context.Context, invocationID string, result mcptransport.Result, resultDigest string) (temporaltools.Invocation, error) {
	if s == nil || s.db == nil {
		return temporaltools.Invocation{}, errors.New("postgres store is not configured")
	}
	digest, err := temporaltools.ResultDigest(result)
	if err != nil {
		return temporaltools.Invocation{}, err
	}
	if digest != resultDigest {
		return temporaltools.Invocation{}, temporaltools.ErrInvocationConflict
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return temporaltools.Invocation{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE agent_server_tool_invocations
		SET state = 'COMPLETE',
		    result_json = $2,
		    result_digest = $3,
		    last_error = NULL,
		    updated_at = NOW()
		WHERE invocation_id = $1
		  AND state <> 'COMPLETE'
		RETURNING `+postgresInvocationColumns,
		invocationID,
		payload,
		resultDigest,
	)
	stored, scanErr := scanInvocation(row)
	if scanErr == nil {
		return stored, nil
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return temporaltools.Invocation{}, fmt.Errorf("complete tool invocation: %w", scanErr)
	}
	stored, err = s.Get(ctx, invocationID)
	if err != nil {
		return temporaltools.Invocation{}, err
	}
	if stored.State != temporaltools.InvocationComplete || stored.Result == nil || stored.ResultDigest != resultDigest {
		return temporaltools.Invocation{}, temporaltools.ErrInvocationConflict
	}
	return stored, nil
}

func (s *PostgresStore) Fail(ctx context.Context, invocationID, message string) error {
	if s == nil || s.db == nil {
		return errors.New("postgres store is not configured")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_server_tool_invocations
		SET state = 'FAILED',
		    last_error = $2,
		    updated_at = NOW()
		WHERE invocation_id = $1
		  AND state <> 'COMPLETE'`,
		invocationID,
		message,
	)
	if err != nil {
		return fmt.Errorf("fail tool invocation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		if _, getErr := s.Get(ctx, invocationID); getErr != nil {
			return getErr
		}
	}
	return nil
}

const postgresInvocationColumns = `
	invocation_id,
	run_id,
	conversation_id,
	tool_name,
	tool_protocol,
	tool_endpoint,
	tool_read_only,
	snapshot_digest,
	arguments_json,
	arguments_digest,
	state,
	result_json,
	result_digest,
	last_error,
	created_at,
	updated_at`

func scanInvocation(scanner rowScanner) (temporaltools.Invocation, error) {
	var (
		invocationID    string
		runID           string
		conversationID  string
		toolName        string
		toolProtocol    string
		toolEndpoint    string
		toolReadOnly    bool
		snapshotDigest  string
		argumentsJSON   []byte
		argumentsDigest string
		state           string
		resultJSON      []byte
		resultDigest    sql.NullString
		lastError       sql.NullString
		createdAt       time.Time
		updatedAt       time.Time
	)
	if err := scanner.Scan(
		&invocationID,
		&runID,
		&conversationID,
		&toolName,
		&toolProtocol,
		&toolEndpoint,
		&toolReadOnly,
		&snapshotDigest,
		&argumentsJSON,
		&argumentsDigest,
		&state,
		&resultJSON,
		&resultDigest,
		&lastError,
		&createdAt,
		&updatedAt,
	); err != nil {
		return temporaltools.Invocation{}, err
	}
	_ = createdAt
	_ = updatedAt

	arguments := map[string]any{}
	if len(argumentsJSON) > 0 {
		if err := json.Unmarshal(argumentsJSON, &arguments); err != nil {
			return temporaltools.Invocation{}, fmt.Errorf("decode invocation arguments: %w", err)
		}
	}
	invocation := temporaltools.Invocation{
		Ref: temporaltools.InvocationRef{
			InvocationID:   invocationID,
			RunID:          runtimesdk.RunID(runID),
			ConversationID: runtimesdk.ConversationID(conversationID),
			Tool: runtimesdk.ToolDescriptor{
				Name:           runtimesdk.ToolName(toolName),
				Protocol:       runtimesdk.ToolProtocol(toolProtocol),
				Endpoint:       toolEndpoint,
				ReadOnly:       toolReadOnly,
				SnapshotDigest: snapshotDigest,
			},
			ArgumentsDigest: argumentsDigest,
		},
		Arguments: arguments,
		State:     temporaltools.InvocationState(state),
	}
	if resultDigest.Valid {
		invocation.ResultDigest = resultDigest.String
	}
	if lastError.Valid {
		invocation.LastError = lastError.String
	}
	if len(resultJSON) > 0 {
		var result mcptransport.Result
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			return temporaltools.Invocation{}, fmt.Errorf("decode invocation result: %w", err)
		}
		invocation.Result = &result
	}
	if err := invocation.Ref.Validate(); err != nil {
		return temporaltools.Invocation{}, fmt.Errorf("postgres contains invalid invocation: %w", err)
	}
	switch invocation.State {
	case temporaltools.InvocationPrepared, temporaltools.InvocationFailed, temporaltools.InvocationComplete:
	default:
		return temporaltools.Invocation{}, fmt.Errorf("postgres contains invalid invocation state %q", invocation.State)
	}
	return invocation, nil
}
