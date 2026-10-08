package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

func (s *PostgresStore) ResolveUnknown(ctx context.Context, lease LeaseToken, resolution EffectResolution, now time.Time) (WorkRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkRecord{}, fmt.Errorf("begin effect reconciliation: %w", err)
	}
	defer tx.Rollback()

	record, err := getPostgresWorkForUpdate(ctx, tx, lease.EventID)
	if err != nil {
		return WorkRecord{}, err
	}
	if !leaseMatches(record, lease, now) {
		return WorkRecord{}, ErrLeaseLost
	}
	if record.LifecycleState != agent.StateUnknown {
		return WorkRecord{}, fmt.Errorf("%w: reconciliation requires UNKNOWN, state=%s", ErrEffectResolution, record.LifecycleState)
	}
	if record.Plan == nil {
		return WorkRecord{}, ErrPlanRequired
	}
	if err := validateResolutionForWork(record, lease, resolution, now); err != nil {
		return WorkRecord{}, err
	}

	resolution.ObservedAt = resolution.ObservedAt.UTC()
	document, err := json.Marshal(resolution)
	if err != nil {
		return WorkRecord{}, fmt.Errorf("marshal effect reconciliation: %w", err)
	}
	target := resolution.targetState()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_runtime_work
		SET effect_resolution_document = $1,
		    lifecycle_state = $2,
		    lifecycle_version = lifecycle_version + 1,
		    lifecycle_updated_at_ns = $3
		WHERE event_id = $4`,
		document,
		string(target),
		now.UnixNano(),
		lease.EventID,
	); err != nil {
		return WorkRecord{}, fmt.Errorf("persist effect reconciliation: %w", err)
	}

	cloned := resolution
	record.Resolution = &cloned
	record.LifecycleState = target
	record.LifecycleVersion++
	record.LifecycleUpdatedAt = now.UTC()

	if err := tx.Commit(); err != nil {
		return WorkRecord{}, fmt.Errorf("commit effect reconciliation: %w", err)
	}
	return record, nil
}
