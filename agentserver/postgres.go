package agentserver

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

//go:embed postgres_schema.sql
var postgresSchema string

const defaultPostgresWatchPollInterval = time.Second

// PostgresStore persists Agent Server conversations, run bindings, and ordered
// event history. Durable execution itself remains owned by ExecutionBackend.
type PostgresStore struct {
	db           *sql.DB
	pollInterval time.Duration
}

var _ Store = (*PostgresStore)(nil)

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresStore{db: db, pollInterval: defaultPostgresWatchPollInterval}, nil
}

func (s *PostgresStore) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("postgres store is not configured")
	}
	if _, err := s.db.ExecContext(ctx, postgresSchema); err != nil {
		return fmt.Errorf("migrate agent server postgres store: %w", err)
	}
	return nil
}

func (s *PostgresStore) PutConversation(ctx context.Context, conversation Conversation) (Conversation, bool, error) {
	if s == nil || s.db == nil {
		return Conversation{}, false, errors.New("postgres store is not configured")
	}
	if err := conversation.Validate(); err != nil {
		return Conversation{}, false, err
	}

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_server_conversations (
			conversation_id, agent_id, workspace_id, created_at
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (conversation_id) DO NOTHING
		RETURNING conversation_id, agent_id, workspace_id, created_at`,
		string(conversation.Ref.ID),
		string(conversation.Ref.AgentID),
		string(conversation.Ref.WorkspaceID),
		conversation.CreatedAt.UTC(),
	)
	stored, err := scanConversation(row)
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, false, fmt.Errorf("insert conversation: %w", err)
	}

	stored, err = s.GetConversation(ctx, conversation.Ref.ID)
	if err != nil {
		return Conversation{}, false, err
	}
	if stored.Ref != conversation.Ref {
		return Conversation{}, false, fmt.Errorf("%w: conversation %s", ErrConflict, conversation.Ref.ID)
	}
	return stored, false, nil
}

func (s *PostgresStore) GetConversation(ctx context.Context, id runtimesdk.ConversationID) (Conversation, error) {
	if s == nil || s.db == nil {
		return Conversation{}, errors.New("postgres store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT conversation_id, agent_id, workspace_id, created_at
		FROM agent_server_conversations
		WHERE conversation_id = $1`,
		string(id),
	)
	conversation, err := scanConversation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, fmt.Errorf("%w: %s", ErrConversationNotFound, id)
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("get conversation: %w", err)
	}
	return conversation, nil
}

func (s *PostgresStore) PutRun(ctx context.Context, record RunRecord) (RunRecord, bool, error) {
	if s == nil || s.db == nil {
		return RunRecord{}, false, errors.New("postgres store is not configured")
	}
	if err := record.Validate(); err != nil {
		return RunRecord{}, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("begin run insert: %w", err)
	}
	defer tx.Rollback()

	var conversationAgent string
	err = tx.QueryRowContext(ctx, `
		SELECT agent_id
		FROM agent_server_conversations
		WHERE conversation_id = $1`,
		string(record.ConversationID),
	).Scan(&conversationAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, false, fmt.Errorf("%w: %s", ErrConversationNotFound, record.ConversationID)
	}
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("read run conversation: %w", err)
	}
	if conversationAgent != string(record.AgentID) {
		return RunRecord{}, false, fmt.Errorf("%w: run %s agent does not match conversation", ErrConflict, record.ID)
	}

	row := tx.QueryRowContext(ctx, `
		INSERT INTO agent_server_runs (
			run_id, conversation_id, agent_id, input, input_digest,
			backend, external_id, fingerprint, state, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (run_id) DO NOTHING
		RETURNING `+postgresRunColumns,
		string(record.ID),
		string(record.ConversationID),
		string(record.AgentID),
		record.Input,
		record.InputDigest,
		record.Handle.Backend,
		nullString(record.Handle.ExternalID),
		record.Handle.Fingerprint,
		string(record.Handle.State),
		record.CreatedAt.UTC(),
	)
	stored, scanErr := scanRun(row)
	if scanErr == nil {
		if err := tx.Commit(); err != nil {
			return RunRecord{}, false, fmt.Errorf("commit run insert: %w", err)
		}
		return stored, true, nil
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return RunRecord{}, false, fmt.Errorf("insert run: %w", scanErr)
	}

	stored, err = getRunTx(ctx, tx, record.ID)
	if err != nil {
		return RunRecord{}, false, err
	}
	if !sameRunBinding(stored, record) {
		return RunRecord{}, false, fmt.Errorf("%w: run %s", ErrConflict, record.ID)
	}
	if err := tx.Commit(); err != nil {
		return RunRecord{}, false, fmt.Errorf("commit idempotent run read: %w", err)
	}
	return stored, false, nil
}

func (s *PostgresStore) GetRun(ctx context.Context, id runtimesdk.RunID) (RunRecord, error) {
	if s == nil || s.db == nil {
		return RunRecord{}, errors.New("postgres store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT `+postgresRunColumns+`
		FROM agent_server_runs
		WHERE run_id = $1`,
		string(id),
	)
	record, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	if err != nil {
		return RunRecord{}, fmt.Errorf("get run: %w", err)
	}
	return record, nil
}

func (s *PostgresStore) AppendEvent(ctx context.Context, event runtimesdk.EventEnvelope) (runtimesdk.EventEnvelope, error) {
	if s == nil || s.db == nil {
		return runtimesdk.EventEnvelope{}, errors.New("postgres store is not configured")
	}
	probe := event
	probe.Sequence = 1
	if err := probe.Validate(); err != nil {
		return runtimesdk.EventEnvelope{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("begin event append: %w", err)
	}
	defer tx.Rollback()

	var runConversation string
	err = tx.QueryRowContext(ctx, `
		SELECT conversation_id
		FROM agent_server_runs
		WHERE run_id = $1`,
		string(event.RunID),
	).Scan(&runConversation)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: %s", ErrRunNotFound, event.RunID)
	}
	if err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("read event run binding: %w", err)
	}
	if runConversation != string(event.ConversationID) {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: run %s does not belong to conversation %s", ErrConflict, event.RunID, event.ConversationID)
	}

	var sequence int64
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_server_conversations
		SET next_event_sequence = next_event_sequence + 1
		WHERE conversation_id = $1
		RETURNING next_event_sequence`,
		string(event.ConversationID),
	).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: %s", ErrConversationNotFound, event.ConversationID)
	}
	if err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("allocate event sequence: %w", err)
	}
	if sequence <= 0 {
		return runtimesdk.EventEnvelope{}, errors.New("postgres allocated a non-positive event sequence")
	}

	event.Sequence = uint64(sequence)
	if err := event.Validate(); err != nil {
		return runtimesdk.EventEnvelope{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_server_events (
			conversation_id, sequence, run_id, event_type, occurred_at, payload
		)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		string(event.ConversationID),
		sequence,
		string(event.RunID),
		string(event.Type),
		event.OccurredAt.UTC(),
		[]byte(event.Payload),
	); err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("insert event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("commit event append: %w", err)
	}
	return event, nil
}

func (s *PostgresStore) ListEvents(ctx context.Context, conversationID runtimesdk.ConversationID, after uint64, limit int) ([]runtimesdk.EventEnvelope, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("postgres store is not configured")
	}
	if limit <= 0 {
		return nil, errors.New("event list limit must be positive")
	}
	if after > math.MaxInt64 {
		return nil, errors.New("event cursor exceeds postgres bigint range")
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_server_conversations WHERE conversation_id = $1)`,
		string(conversationID),
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check conversation: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrConversationNotFound, conversationID)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, run_id, event_type, occurred_at, payload
		FROM agent_server_events
		WHERE conversation_id = $1 AND sequence > $2
		ORDER BY sequence
		LIMIT $3`,
		string(conversationID),
		int64(after),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	events := make([]runtimesdk.EventEnvelope, 0)
	for rows.Next() {
		var (
			sequence   int64
			runID      string
			eventType  string
			occurredAt time.Time
			payload    []byte
		)
		if err := rows.Scan(&sequence, &runID, &eventType, &occurredAt, &payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if sequence <= 0 {
			return nil, errors.New("postgres event sequence is non-positive")
		}
		event := runtimesdk.EventEnvelope{
			Sequence:       uint64(sequence),
			Type:           runtimesdk.EventType(eventType),
			RunID:          runtimesdk.RunID(runID),
			ConversationID: conversationID,
			OccurredAt:     occurredAt.UTC(),
			Payload:        append([]byte(nil), payload...),
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("postgres contains invalid event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

// Watch is an advisory wake-up source, not event evidence. It deliberately
// polls rather than pinning a PostgreSQL LISTEN connection per SSE client.
// The SSE path always re-reads ordered persisted events via ListEvents, so
// restarts and writes from another server process remain visible.
func (s *PostgresStore) Watch(ctx context.Context, conversationID runtimesdk.ConversationID) (<-chan struct{}, func(), error) {
	if s == nil || s.db == nil {
		return nil, nil, errors.New("postgres store is not configured")
	}
	if _, err := s.GetConversation(ctx, conversationID); err != nil {
		return nil, nil, err
	}
	interval := s.pollInterval
	if interval <= 0 {
		interval = defaultPostgresWatchPollInterval
	}

	ch := make(chan struct{}, 1)
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() { close(stop) })
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				cancel()
				return
			case <-stop:
				return
			case <-ticker.C:
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()

	return ch, cancel, nil
}

const postgresRunColumns = `
	run_id,
	conversation_id,
	agent_id,
	input,
	input_digest,
	backend,
	external_id,
	fingerprint,
	state,
	created_at`

type rowScanner interface {
	Scan(...any) error
}

func scanConversation(scanner rowScanner) (Conversation, error) {
	var (
		conversationID string
		agentID        string
		workspaceID    string
		createdAt      time.Time
	)
	if err := scanner.Scan(&conversationID, &agentID, &workspaceID, &createdAt); err != nil {
		return Conversation{}, err
	}
	conversation := Conversation{
		Ref: runtimesdk.ConversationRef{
			ID:          runtimesdk.ConversationID(conversationID),
			AgentID:     runtimesdk.AgentID(agentID),
			WorkspaceID: runtimesdk.WorkspaceID(workspaceID),
		},
		CreatedAt: createdAt.UTC(),
	}
	if err := conversation.Validate(); err != nil {
		return Conversation{}, fmt.Errorf("postgres contains invalid conversation: %w", err)
	}
	return conversation, nil
}

func scanRun(scanner rowScanner) (RunRecord, error) {
	var (
		runID          string
		conversationID string
		agentID        string
		input          string
		inputDigest    string
		backend        string
		externalID     sql.NullString
		fingerprint    string
		state          string
		createdAt      time.Time
	)
	if err := scanner.Scan(
		&runID,
		&conversationID,
		&agentID,
		&input,
		&inputDigest,
		&backend,
		&externalID,
		&fingerprint,
		&state,
		&createdAt,
	); err != nil {
		return RunRecord{}, err
	}
	record := RunRecord{
		ID:             runtimesdk.RunID(runID),
		ConversationID: runtimesdk.ConversationID(conversationID),
		AgentID:        runtimesdk.AgentID(agentID),
		Input:          input,
		InputDigest:    inputDigest,
		Handle: runtimesdk.RunHandle{
			ID:          runtimesdk.RunID(runID),
			Backend:     backend,
			Fingerprint: fingerprint,
			State:       runtimesdk.RunState(state),
		},
		CreatedAt: createdAt.UTC(),
	}
	if externalID.Valid {
		record.Handle.ExternalID = externalID.String
	}
	if err := record.Validate(); err != nil {
		return RunRecord{}, fmt.Errorf("postgres contains invalid run: %w", err)
	}
	return record, nil
}

func getRunTx(ctx context.Context, tx *sql.Tx, id runtimesdk.RunID) (RunRecord, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT `+postgresRunColumns+`
		FROM agent_server_runs
		WHERE run_id = $1`,
		string(id),
	)
	record, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	if err != nil {
		return RunRecord{}, fmt.Errorf("get run in transaction: %w", err)
	}
	return record, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
