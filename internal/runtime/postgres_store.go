package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
 "github.com/achirothmane/governed-agent-runtime/internal/governance"
)

//go:embed schema/postgres.sql
var postgresSchema string

type PostgresStore struct {
	db *sql.DB
}

var _ Store = (*PostgresStore)(nil)

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, postgresSchema); err != nil {
		return fmt.Errorf("migrate postgres runtime store: %w", err)
	}
	return nil
}

func (s *PostgresStore) Enqueue(ctx context.Context, event Event) (WorkRecord, error) {
	if err := event.Validate(); err != nil {
		return WorkRecord{}, err
	}

	digest, err := digestEvent(event)
	if err != nil {
		return WorkRecord{}, fmt.Errorf("digest event: %w", err)
	}

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_runtime_work (
			event_id,
			agent_id,
			event_kind,
			event_payload,
			event_created_at_ns,
			event_digest,
			work_state,
			lifecycle_state,
			lifecycle_version,
			lifecycle_updated_at_ns,
			lease_epoch
		)
		VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', $7, 1, $5, 0)
		ON CONFLICT (event_id) DO NOTHING
		RETURNING `+postgresColumnsWithDigest,
		event.ID,
		string(event.AgentID),
		event.Kind,
		[]byte(event.Payload),
		event.CreatedAt.UnixNano(),
		digest,
		string(agent.StateSleeping),
	)

	record, storedDigest, err := scanPostgresWork(row, true)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkRecord{}, fmt.Errorf("insert event: %w", err)
	}

	record, storedDigest, err = s.getWithDigest(ctx, event.ID)
	if err != nil {
		return WorkRecord{}, err
	}
	if !bytes.Equal(storedDigest, digest) {
		return WorkRecord{}, ErrEventConflict
	}
	return record, nil
}

func (s *PostgresStore) Claim(ctx context.Context, workerID string, now time.Time, ttl time.Duration) (ClaimedWork, error) {
	if err := validateWorkerAndTTL(workerID, ttl); err != nil {
		return ClaimedWork{}, err
	}

	expiresAt := now.Add(ttl)
	row := s.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT event_id, work_state, lifecycle_state
			FROM agent_runtime_work
			WHERE work_state = 'PENDING'
			   OR (work_state = 'LEASED' AND lease_expires_at_ns <= $1)
			ORDER BY sequence
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE agent_runtime_work AS w
		SET
			work_state = 'LEASED',
			lease_owner = $2,
			lease_epoch = w.lease_epoch + 1,
			lease_expires_at_ns = $3,
			completed_at_ns = NULL,
			lifecycle_state = CASE
				WHEN candidate.work_state = 'LEASED'
				 AND candidate.lifecycle_state = 'EXECUTING'
				THEN 'UNKNOWN'
				ELSE w.lifecycle_state
			END,
			lifecycle_version = w.lifecycle_version + CASE
				WHEN candidate.work_state = 'LEASED'
				 AND candidate.lifecycle_state = 'EXECUTING'
				THEN 1
				ELSE 0
			END,
			lifecycle_updated_at_ns = CASE
				WHEN candidate.work_state = 'LEASED'
				 AND candidate.lifecycle_state = 'EXECUTING'
				THEN $1
				ELSE w.lifecycle_updated_at_ns
			END
		FROM candidate
		WHERE w.event_id = candidate.event_id
		RETURNING `+postgresReturningColumns,
		now.UnixNano(),
		workerID,
		expiresAt.UnixNano(),
	)

	record, _, err := scanPostgresWork(row, false)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimedWork{}, ErrNoWork
	}
	if err != nil {
		return ClaimedWork{}, fmt.Errorf("claim work: %w", err)
	}

	return ClaimedWork{
		Record: record,
		Lease: LeaseToken{
			EventID:   record.Event.ID,
			WorkerID:  record.LeaseOwner,
			Epoch:     record.LeaseEpoch,
			ExpiresAt: record.LeaseExpiresAt,
		},
	}, nil
}

func (s *PostgresStore) Renew(ctx context.Context, lease LeaseToken, now time.Time, ttl time.Duration) (LeaseToken, error) {
	if err := validateWorkerAndTTL(lease.WorkerID, ttl); err != nil {
		return LeaseToken{}, err
	}

	expiresAt := now.Add(ttl)
	var expiresAtNS int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE agent_runtime_work
		SET lease_expires_at_ns = $1
		WHERE event_id = $2
		  AND work_state = 'LEASED'
		  AND lease_owner = $3
		  AND lease_epoch = $4
		  AND lease_expires_at_ns > $5
		RETURNING lease_expires_at_ns`,
		expiresAt.UnixNano(),
		lease.EventID,
		lease.WorkerID,
		int64(lease.Epoch),
		now.UnixNano(),
	).Scan(&expiresAtNS)
	if errors.Is(err, sql.ErrNoRows) {
		return LeaseToken{}, s.classifyLeaseMiss(ctx, lease.EventID)
	}
	if err != nil {
		return LeaseToken{}, fmt.Errorf("renew lease: %w", err)
	}

	renewed := lease
	renewed.ExpiresAt = time.Unix(0, expiresAtNS).UTC()
	return renewed, nil
}

func (s *PostgresStore) BindPlan(ctx context.Context, lease LeaseToken, binding PlanBinding, now time.Time) (WorkRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkRecord{}, fmt.Errorf("begin plan binding: %w", err)
	}
	defer tx.Rollback()

	record, err := getPostgresWorkForUpdate(ctx, tx, lease.EventID)
	if err != nil {
		return WorkRecord{}, err
	}
	if !leaseMatches(record, lease, now) {
		return WorkRecord{}, ErrLeaseLost
	}
	if record.LifecycleState != agent.StatePlanning {
		return WorkRecord{}, fmt.Errorf("%w: plan may only be bound while PLANNING, state=%s", ErrPlanBindingInvalid, record.LifecycleState)
	}
	if err := validatePlanBindingForWork(record, binding); err != nil {
		return WorkRecord{}, err
	}
	if record.Plan != nil {
		if samePlanBinding(*record.Plan, binding) {
			return record, nil
		}
		return WorkRecord{}, ErrPlanConflict
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runtime_work
		SET plan_id = $1,
		    plan_agent_id = $2,
		    plan_event_id = $3,
		    plan_digest = $4,
		    plan_document = $5,
		    plan_bound_at_ns = $6
		WHERE event_id = $7`,
		binding.PlanID,
		string(binding.AgentID),
		binding.EventID,
		binding.Digest,
		[]byte(binding.Document),
		now.UnixNano(),
		lease.EventID,
	); err != nil {
		return WorkRecord{}, fmt.Errorf("persist plan binding: %w", err)
	}

	cloned := binding.Clone()
	boundAt := now.UTC()
	record.Plan = &cloned
	record.PlanBoundAt = &boundAt

	if err := tx.Commit(); err != nil {
		return WorkRecord{}, fmt.Errorf("commit plan binding: %w", err)
	}
	return record, nil
}

func (s *PostgresStore) Transition(ctx context.Context, lease LeaseToken, target agent.State, now time.Time) (WorkRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkRecord{}, fmt.Errorf("begin lifecycle transition: %w", err)
	}
	defer tx.Rollback()

	record, err := getPostgresWorkForUpdate(ctx, tx, lease.EventID)
	if err != nil {
		return WorkRecord{}, err
	}
	if !leaseMatches(record, lease, now) {
		return WorkRecord{}, ErrLeaseLost
	}
	if !agent.CanTransition(record.LifecycleState, target) {
		return WorkRecord{}, fmt.Errorf("%w: %s -> %s", ErrInvalidLifecycleTransition, record.LifecycleState, target)
	}
	if target == agent.StateExecuting {
 return WorkRecord{}, governance.ErrAdmission
 }
 if target == agent.StateWaitingForAdmission && record.Plan == nil {
		return WorkRecord{}, ErrPlanRequired
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runtime_work
		SET lifecycle_state = $1,
		    lifecycle_version = lifecycle_version + 1,
		    lifecycle_updated_at_ns = $2
		WHERE event_id = $3`,
		string(target),
		now.UnixNano(),
		lease.EventID,
	); err != nil {
		return WorkRecord{}, fmt.Errorf("persist lifecycle transition: %w", err)
	}

	record.LifecycleState = target
	record.LifecycleVersion++
	record.LifecycleUpdatedAt = now.UTC()

	if err := tx.Commit(); err != nil {
		return WorkRecord{}, fmt.Errorf("commit lifecycle transition: %w", err)
	}
	return record, nil
}

func (s *PostgresStore) Complete(ctx context.Context, lease LeaseToken, now time.Time) (WorkRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkRecord{}, fmt.Errorf("begin completion: %w", err)
	}
	defer tx.Rollback()

	record, err := getPostgresWorkForUpdate(ctx, tx, lease.EventID)
	if err != nil {
		return WorkRecord{}, err
	}
	if !leaseMatches(record, lease, now) {
		return WorkRecord{}, ErrLeaseLost
	}
	if record.LifecycleState != agent.StateSleeping {
		return WorkRecord{}, fmt.Errorf("%w: state=%s", ErrLifecycleIncomplete, record.LifecycleState)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runtime_work
		SET work_state = 'DONE',
		    completed_at_ns = $1
		WHERE event_id = $2`,
		now.UnixNano(),
		lease.EventID,
	); err != nil {
		return WorkRecord{}, fmt.Errorf("persist completion: %w", err)
	}

	completedAt := now.UTC()
	record.State = WorkDone
	record.CompletedAt = &completedAt

	if err := tx.Commit(); err != nil {
		return WorkRecord{}, fmt.Errorf("commit completion: %w", err)
	}
	return record, nil
}

func (s *PostgresStore) Get(ctx context.Context, eventID string) (WorkRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+postgresColumns+`
		FROM agent_runtime_work
		WHERE event_id = $1`,
		eventID,
	)
	record, _, err := scanPostgresWork(row, false)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkRecord{}, ErrWorkNotFound
	}
	if err != nil {
		return WorkRecord{}, fmt.Errorf("get work: %w", err)
	}
	return record, nil
}

func (s *PostgresStore) getWithDigest(ctx context.Context, eventID string) (WorkRecord, []byte, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+postgresColumnsWithDigest+`
		FROM agent_runtime_work
		WHERE event_id = $1`,
		eventID,
	)
	record, digest, err := scanPostgresWork(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkRecord{}, nil, ErrWorkNotFound
	}
	if err != nil {
		return WorkRecord{}, nil, fmt.Errorf("get work with digest: %w", err)
	}
	return record, digest, nil
}

func (s *PostgresStore) classifyLeaseMiss(ctx context.Context, eventID string) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_runtime_work WHERE event_id = $1)`,
		eventID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("classify lease miss: %w", err)
	}
	if !exists {
		return ErrWorkNotFound
	}
	return ErrLeaseLost
}

func getPostgresWorkForUpdate(ctx context.Context, tx *sql.Tx, eventID string) (WorkRecord, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT `+postgresColumns+`
		FROM agent_runtime_work
		WHERE event_id = $1
		FOR UPDATE`,
		eventID,
	)
	record, _, err := scanPostgresWork(row, false)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkRecord{}, ErrWorkNotFound
	}
	if err != nil {
		return WorkRecord{}, fmt.Errorf("lock work: %w", err)
	}
	return record, nil
}

type rowScanner interface {
	Scan(...any) error
}

const postgresColumns = `
	sequence,
	event_id,
	agent_id,
	event_kind,
	event_payload,
	event_created_at_ns,
	work_state,
	lifecycle_state,
	lifecycle_version,
	lifecycle_updated_at_ns,
	plan_id,
	plan_agent_id,
	plan_event_id,
	plan_digest,
	plan_document,
	plan_bound_at_ns,
	lease_owner,
	lease_epoch,
	lease_expires_at_ns,
	completed_at_ns, admission_document`

const postgresColumnsWithDigest = postgresColumns + `,
	event_digest`

const postgresReturningColumns = `
	w.sequence,
	w.event_id,
	w.agent_id,
	w.event_kind,
	w.event_payload,
	w.event_created_at_ns,
	w.work_state,
	w.lifecycle_state,
	w.lifecycle_version,
	w.lifecycle_updated_at_ns,
	w.plan_id,
	w.plan_agent_id,
	w.plan_event_id,
	w.plan_digest,
	w.plan_document,
	w.plan_bound_at_ns,
	w.lease_owner,
	w.lease_epoch,
	w.lease_expires_at_ns,
	w.completed_at_ns, w.admission_document`

func scanPostgresWork(scanner rowScanner, withDigest bool) (WorkRecord, []byte, error) {
	var (
		sequence             int64
		eventID              string
		agentID              string
		eventKind            string
		eventPayload         []byte
		eventCreatedAtNS     int64
		workState            string
		lifecycleState       string
		lifecycleVersion     int64
		lifecycleUpdatedAtNS int64
		planID               sql.NullString
		planAgentID          sql.NullString
		planEventID          sql.NullString
		planDigest           []byte
		planDocument         []byte
		planBoundAtNS        sql.NullInt64
		leaseOwner           sql.NullString
		leaseEpoch           int64
		leaseExpiresAtNS     sql.NullInt64
		completedAtNS        sql.NullInt64
		admissionDocument []byte
  eventDigest          []byte
	)

	dest := []any{
		&sequence,
		&eventID,
		&agentID,
		&eventKind,
		&eventPayload,
		&eventCreatedAtNS,
		&workState,
		&lifecycleState,
		&lifecycleVersion,
		&lifecycleUpdatedAtNS,
		&planID,
		&planAgentID,
		&planEventID,
		&planDigest,
		&planDocument,
		&planBoundAtNS,
		&leaseOwner,
		&leaseEpoch,
		&leaseExpiresAtNS,
		&completedAtNS,
  &admissionDocument,
	}
	if withDigest {
		dest = append(dest, &eventDigest)
	}

	if err := scanner.Scan(dest...); err != nil {
		return WorkRecord{}, nil, err
	}
	if sequence < 0 || lifecycleVersion < 0 || leaseEpoch < 0 {
		return WorkRecord{}, nil, errors.New("postgres runtime store contains negative monotonic counters")
	}

	record := WorkRecord{
		Sequence: uint64(sequence),
		Event: Event{
			ID:        eventID,
			AgentID:   agent.AgentID(agentID),
			Kind:      eventKind,
			Payload:   json.RawMessage(eventPayload),
			CreatedAt: time.Unix(0, eventCreatedAtNS).UTC(),
		},
		State:              WorkState(workState),
		LifecycleState:     agent.State(lifecycleState),
		LifecycleVersion:   uint64(lifecycleVersion),
		LifecycleUpdatedAt: time.Unix(0, lifecycleUpdatedAtNS).UTC(),
		LeaseEpoch:         uint64(leaseEpoch),
	}

	hasAnyPlanField := planID.Valid || planAgentID.Valid || planEventID.Valid || len(planDigest) > 0 || len(planDocument) > 0 || planBoundAtNS.Valid
	hasAllPlanFields := planID.Valid && planAgentID.Valid && planEventID.Valid && len(planDigest) > 0 && len(planDocument) > 0 && planBoundAtNS.Valid
	if hasAnyPlanField && !hasAllPlanFields {
		return WorkRecord{}, nil, errors.New("postgres runtime store contains partial plan binding")
	}
	if hasAllPlanFields {
		binding := PlanBinding{
			PlanID:   planID.String,
			AgentID:  agent.AgentID(planAgentID.String),
			EventID:  planEventID.String,
			Digest:   append([]byte(nil), planDigest...),
			Document: append([]byte(nil), planDocument...),
		}
		if err := binding.Validate(); err != nil {
			return WorkRecord{}, nil, fmt.Errorf("postgres runtime store contains invalid plan binding: %w", err)
		}
		if binding.AgentID != record.Event.AgentID || binding.EventID != record.Event.ID {
			return WorkRecord{}, nil, errors.New("postgres runtime store contains plan binding for different work identity")
		}
		boundAt := time.Unix(0, planBoundAtNS.Int64).UTC()
		record.Plan = &binding
		record.PlanBoundAt = &boundAt
	}

	if leaseOwner.Valid {
		record.LeaseOwner = leaseOwner.String
	}
	if leaseExpiresAtNS.Valid {
		record.LeaseExpiresAt = time.Unix(0, leaseExpiresAtNS.Int64).UTC()
	}
	if completedAtNS.Valid {
		completedAt := time.Unix(0, completedAtNS.Int64).UTC()
		record.CompletedAt = &completedAt
	}

	if len(admissionDocument) > 0 {
 var signed governance.SignedAdmission
 if err := json.Unmarshal(admissionDocument, &signed); err != nil { return WorkRecord{}, nil, err }
 record.Admission = &signed
 }
 return record, eventDigest, nil
}

func digestEvent(event Event) ([]byte, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	return sum[:], nil
}


func (s *PostgresStore) BeginExecution(ctx context.Context, lease LeaseToken, signed governance.SignedAdmission, verifier governance.Verifier, now time.Time) (WorkRecord, error) {
 signed = governance.SignedAdmission{Document: append([]byte(nil), signed.Document...), Signature: append([]byte(nil), signed.Signature...)}
 tx, err := s.db.BeginTx(ctx, nil)
 if err != nil { return WorkRecord{}, err }
 defer tx.Rollback()
 r, err := getPostgresWorkForUpdate(ctx, tx, lease.EventID)
 if err != nil { return WorkRecord{}, err }
 if err := verifyAdmission(ctx, r, lease, signed, verifier, now); err != nil { return WorkRecord{}, err }
 document, err := json.Marshal(signed)
 if err != nil { return WorkRecord{}, err }
 if _, err := tx.ExecContext(ctx, `UPDATE agent_runtime_work SET admission_document = $1, lifecycle_state = 'EXECUTING', lifecycle_version = lifecycle_version + 1, lifecycle_updated_at_ns = $2 WHERE event_id = $3`, document, now.UnixNano(), lease.EventID); err != nil { return WorkRecord{}, err }
 r.Admission = &governance.SignedAdmission{Document: append([]byte(nil), signed.Document...), Signature: append([]byte(nil), signed.Signature...)}
 r.LifecycleState = agent.StateExecuting
 r.LifecycleVersion++
 r.LifecycleUpdatedAt = now
 if err := tx.Commit(); err != nil { return WorkRecord{}, err }
 return r, nil
}
