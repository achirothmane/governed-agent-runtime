package agentserver

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

//go:embed schema/postgres.sql
var postgresSchema string

// PostgresStore is the restart-durable Agent Server metadata/event store.
// Durable execution remains owned by ExecutionBackend.
type PostgresStore struct {
	db *sql.DB

	watchMu     sync.Mutex
	watchers    map[runtimesdk.ConversationID]map[uint64]chan struct{}
	nextWatcher uint64
}

var _ Store = (*PostgresStore)(nil)

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresStore{
		db:       db,
		watchers: make(map[runtimesdk.ConversationID]map[uint64]chan struct{}),
	}, nil
}

func (s *PostgresStore) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("postgres store is nil")
	}
	if _, err := s.db.ExecContext(ctx, postgresSchema); err != nil {
		return fmt.Errorf("migrate agent server store: %w", err)
	}
	return nil
}

func (s *PostgresStore) PutConversation(ctx context.Context, conversation Conversation) (Conversation, bool, error) {
	if s == nil || s.db == nil {
		return Conversation{}, false, errors.New("postgres store is nil")
	}
	if err := conversation.Validate(); err != nil {
		return Conversation{}, false, err
	}
	row := s.db.QueryRowContext(ctx, "INSERT INTO agent_server_conversations (conversation_id, agent_id, workspace_id, created_at_ns) VALUES ($1, $2, $3, $4) ON CONFLICT (conversation_id) DO NOTHING RETURNING conversation_id, agent_id, workspace_id, created_at_ns",
		string(conversation.Ref.ID),
		string(conversation.Ref.AgentID),
		string(conversation.Ref.WorkspaceID),
		conversation.CreatedAt.UnixNano(),
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
		return Conversation{}, errors.New("postgres store is nil")
	}
	row := s.db.QueryRowContext(ctx, "SELECT conversation_id, agent_id, workspace_id, created_at_ns FROM agent_server_conversations WHERE conversation_id = $1", string(id))
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
		return RunRecord{}, false, errors.New("postgres store is nil")
	}
	if err := validateRunRecord(record); err != nil {
		return RunRecord{}, false, err
	}
	handleJSON, err := json.Marshal(record.Handle)
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("encode run handle: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunRecord{}, false, fmt.Errorf("begin run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var conversationAgent string
	if err := tx.QueryRowContext(ctx, "SELECT agent_id FROM agent_server_conversations WHERE conversation_id = $1 FOR SHARE", string(record.ConversationID)).Scan(&conversationAgent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunRecord{}, false, fmt.Errorf("%w: %s", ErrConversationNotFound, record.ConversationID)
		}
		return RunRecord{}, false, fmt.Errorf("bind run conversation: %w", err)
	}
	if runtimesdk.AgentID(conversationAgent) != record.AgentID {
		return RunRecord{}, false, fmt.Errorf("%w: run %s agent does not match conversation", ErrConflict, record.ID)
	}

	row := tx.QueryRowContext(ctx, "INSERT INTO agent_server_runs (run_id, conversation_id, agent_id, input_text, input_digest, handle_json, fingerprint, created_at_ns) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (run_id) DO NOTHING RETURNING run_id, conversation_id, agent_id, input_text, input_digest, handle_json, created_at_ns",
		string(record.ID),
		string(record.ConversationID),
		string(record.AgentID),
		record.Input,
		record.InputDigest,
		handleJSON,
		record.Handle.Fingerprint,
		record.CreatedAt.UnixNano(),
	)
	stored, scanErr := scanRun(row)
	created := scanErr == nil
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return RunRecord{}, false, fmt.Errorf("insert run: %w", scanErr)
	}
	if errors.Is(scanErr, sql.ErrNoRows) {
		stored, err = getRunTx(ctx, tx, record.ID)
		if err != nil {
			return RunRecord{}, false, err
		}
		if stored.ConversationID != record.ConversationID ||
			stored.AgentID != record.AgentID ||
			stored.InputDigest != record.InputDigest ||
			stored.Handle.Fingerprint != record.Handle.Fingerprint {
			return RunRecord{}, false, fmt.Errorf("%w: run %s", ErrConflict, record.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return RunRecord{}, false, fmt.Errorf("commit run: %w", err)
	}
	return stored, created, nil
}

func (s *PostgresStore) GetRun(ctx context.Context, id runtimesdk.RunID) (RunRecord, error) {
	if s == nil || s.db == nil {
		return RunRecord{}, errors.New("postgres store is nil")
	}
	row := s.db.QueryRowContext(ctx, "SELECT run_id, conversation_id, agent_id, input_text, input_digest, handle_json, created_at_ns FROM agent_server_runs WHERE run_id = $1", string(id))
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
		return runtimesdk.EventEnvelope{}, errors.New("postgres store is nil")
	}
	if event.Sequence != 0 {
		return runtimesdk.EventEnvelope{}, errors.New("event sequence is assigned by store")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("begin event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM agent_server_conversations WHERE conversation_id = $1)", string(event.ConversationID)).Scan(&exists); err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("check event conversation: %w", err)
	}
	if !exists {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: %s", ErrConversationNotFound, event.ConversationID)
	}

	if event.RunID != "" {
		var runConversation string
		if err := tx.QueryRowContext(ctx, "SELECT conversation_id FROM agent_server_runs WHERE run_id = $1", string(event.RunID)).Scan(&runConversation); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: %s", ErrRunNotFound, event.RunID)
			}
			return runtimesdk.EventEnvelope{}, fmt.Errorf("check event run: %w", err)
		}
		if runtimesdk.ConversationID(runConversation) != event.ConversationID {
			return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: run %s belongs to conversation %s", ErrConflict, event.RunID, runConversation)
		}
	}

	var sequence uint64
	if err := tx.QueryRowContext(ctx, "INSERT INTO agent_server_event_cursors (conversation_id, last_sequence) VALUES ($1, 1) ON CONFLICT (conversation_id) DO UPDATE SET last_sequence = agent_server_event_cursors.last_sequence + 1 RETURNING last_sequence", string(event.ConversationID)).Scan(&sequence); err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("allocate event sequence: %w", err)
	}
	event.Sequence = sequence
	if err := event.Validate(); err != nil {
		return runtimesdk.EventEnvelope{}, err
	}
	payload := event.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO agent_server_events (conversation_id, sequence, event_type, run_id, occurred_at_ns, payload_json) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6)",
		string(event.ConversationID),
		event.Sequence,
		string(event.Type),
		string(event.RunID),
		event.OccurredAt.UnixNano(),
		[]byte(payload),
	); err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("insert event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("commit event: %w", err)
	}
	s.notify(event.ConversationID)
	return event, nil
}

func (s *PostgresStore) ListEvents(ctx context.Context, id runtimesdk.ConversationID, after uint64, limit int) ([]runtimesdk.EventEnvelope, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("postgres store is nil")
	}
	if limit <= 0 {
		return nil, errors.New("event limit must be positive")
	}
	if limit > 1000 {
		limit = 1000
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM agent_server_conversations WHERE conversation_id = $1)", string(id)).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check conversation: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrConversationNotFound, id)
	}

	rows, err := s.db.QueryContext(ctx, "SELECT sequence, event_type, run_id, occurred_at_ns, payload_json FROM agent_server_events WHERE conversation_id = $1 AND sequence > $2 ORDER BY sequence LIMIT $3", string(id), after, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	events := make([]runtimesdk.EventEnvelope, 0)
	for rows.Next() {
		var (
			event      runtimesdk.EventEnvelope
			eventType  string
			runID      sql.NullString
			occurredNS int64
			payload    []byte
		)
		if err := rows.Scan(&event.Sequence, &eventType, &runID, &occurredNS, &payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		event.Type = runtimesdk.EventType(eventType)
		event.ConversationID = id
		if runID.Valid {
			event.RunID = runtimesdk.RunID(runID.String)
		}
		event.OccurredAt = time.Unix(0, occurredNS).UTC()
		event.Payload = append(json.RawMessage(nil), payload...)
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("stored event %d is invalid: %w", event.Sequence, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

func (s *PostgresStore) Watch(ctx context.Context, id runtimesdk.ConversationID) (<-chan struct{}, func(), error) {
	if s == nil || s.db == nil {
		return nil, nil, errors.New("postgres store is nil")
	}
	if _, err := s.GetConversation(ctx, id); err != nil {
		return nil, nil, err
	}
	ch := make(chan struct{}, 1)
	s.watchMu.Lock()
	s.nextWatcher++
	watcherID := s.nextWatcher
	if s.watchers[id] == nil {
		s.watchers[id] = make(map[uint64]chan struct{})
	}
	s.watchers[id][watcherID] = ch
	s.watchMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.watchMu.Lock()
			defer s.watchMu.Unlock()
			watchers := s.watchers[id]
			if watchers == nil {
				return
			}
			delete(watchers, watcherID)
			if len(watchers) == 0 {
				delete(s.watchers, id)
			}
		})
	}
	return ch, cancel, nil
}

func (s *PostgresStore) notify(id runtimesdk.ConversationID) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	for _, ch := range s.watchers[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type scanner interface {
	Scan(...any) error
}

func scanConversation(row scanner) (Conversation, error) {
	var (
		conversationID string
		agentID        string
		workspaceID    string
		createdAtNS    int64
	)
	if err := row.Scan(&conversationID, &agentID, &workspaceID, &createdAtNS); err != nil {
		return Conversation{}, err
	}
	return Conversation{
		Ref: runtimesdk.ConversationRef{
			ID:          runtimesdk.ConversationID(conversationID),
			AgentID:     runtimesdk.AgentID(agentID),
			WorkspaceID: runtimesdk.WorkspaceID(workspaceID),
		},
		CreatedAt: time.Unix(0, createdAtNS).UTC(),
	}, nil
}

func scanRun(row scanner) (RunRecord, error) {
	var (
		runID          string
		conversationID string
		agentID        string
		input          string
		inputDigest    string
		handleJSON     []byte
		createdAtNS    int64
	)
	if err := row.Scan(&runID, &conversationID, &agentID, &input, &inputDigest, &handleJSON, &createdAtNS); err != nil {
		return RunRecord{}, err
	}
	var handle runtimesdk.RunHandle
	if err := json.Unmarshal(handleJSON, &handle); err != nil {
		return RunRecord{}, fmt.Errorf("decode run handle: %w", err)
	}
	record := RunRecord{
		ID:             runtimesdk.RunID(runID),
		ConversationID: runtimesdk.ConversationID(conversationID),
		AgentID:        runtimesdk.AgentID(agentID),
		Input:          input,
		InputDigest:    inputDigest,
		Handle:         handle,
		CreatedAt:      time.Unix(0, createdAtNS).UTC(),
	}
	if err := validateRunRecord(record); err != nil {
		return RunRecord{}, fmt.Errorf("stored run is invalid: %w", err)
	}
	return record, nil
}

func getRunTx(ctx context.Context, tx *sql.Tx, id runtimesdk.RunID) (RunRecord, error) {
	row := tx.QueryRowContext(ctx, "SELECT run_id, conversation_id, agent_id, input_text, input_digest, handle_json, created_at_ns FROM agent_server_runs WHERE run_id = $1", string(id))
	record, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	if err != nil {
		return RunRecord{}, fmt.Errorf("get run in transaction: %w", err)
	}
	return record, nil
}

func validateRunRecord(record RunRecord) error {
	if record.ID == "" || record.ConversationID == "" || record.AgentID == "" ||
		record.InputDigest == "" || record.CreatedAt.IsZero() {
		return errors.New("run record is incomplete")
	}
	if record.Handle.ID != record.ID {
		return errors.New("run record handle id mismatch")
	}
	if record.Handle.Backend == "" || record.Handle.Fingerprint == "" || record.Handle.State == "" {
		return errors.New("run record handle is incomplete")
	}
	return nil
}
