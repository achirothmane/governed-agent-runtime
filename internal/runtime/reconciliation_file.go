package runtime

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

func (s *FileStore) ResolveUnknown(ctx context.Context, lease LeaseToken, resolution EffectResolution, now time.Time) (WorkRecord, error) {
	var out WorkRecord
	err := s.withLockedState(ctx, func(state *fileState) (bool, error) {
		r, ok := state.Work[lease.EventID]
		if !ok {
			return false, ErrWorkNotFound
		}
		initializeLifecycle(&r, r.Event.CreatedAt)
		if !leaseMatches(r, lease, now) {
			return false, ErrLeaseLost
		}
		if r.LifecycleState != agent.StateUnknown {
			return false, fmt.Errorf("%w: reconciliation requires UNKNOWN, state=%s", ErrEffectResolution, r.LifecycleState)
		}
		if r.Plan == nil {
			return false, ErrPlanRequired
		}
		if err := validateResolutionForWork(r, lease, resolution, now); err != nil {
			return false, err
		}

		cloned := resolution
		cloned.ObservedAt = resolution.ObservedAt.UTC()
		r.Resolution = &cloned
		r.LifecycleState = resolution.targetState()
		r.LifecycleVersion++
		r.LifecycleUpdatedAt = now.UTC()
		state.Work[r.Event.ID] = r
		out = r
		return true, nil
	})
	return out, err
}

func validateResolutionForWork(record WorkRecord, lease LeaseToken, resolution EffectResolution, now time.Time) error {
	if err := resolution.Validate(); err != nil {
		return err
	}
	if resolution.WorkerID != lease.WorkerID || resolution.LeaseEpoch != lease.Epoch {
		return fmt.Errorf("%w: resolution is not bound to the current lease", ErrEffectResolution)
	}
	if resolution.ObservedAt.After(now) {
		return fmt.Errorf("%w: observation is from the future", ErrEffectResolution)
	}
	if record.Plan == nil || resolution.PlanDigest != hex.EncodeToString(record.Plan.Digest) {
		return fmt.Errorf("%w: resolution plan digest does not match durable plan", ErrEffectResolution)
	}
	return nil
}
