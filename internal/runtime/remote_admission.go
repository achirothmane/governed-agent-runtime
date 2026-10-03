package runtime

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
	"github.com/achirothmane/governed-agent-runtime/internal/governance"
)

// BeginRemoteExecution obtains externally signed admission; the store still
// verifies it using its own configured trust root and live resolver atomically.
// It does not dispatch any effect or retry an ambiguous operation.
func BeginRemoteExecution(ctx context.Context, store Store, lease LeaseToken, client *governance.Client, now time.Time) (WorkRecord, error) {
	if client == nil {
		return WorkRecord{}, governance.ErrAdmission
	}
	record, err := store.Get(ctx, lease.EventID)
	if err != nil {
		return WorkRecord{}, err
	}
	if !leaseMatches(record, lease, now) {
		return WorkRecord{}, ErrLeaseLost
	}
	if record.LifecycleState != agent.StateWaitingForAdmission {
		return WorkRecord{}, ErrInvalidLifecycleTransition
	}
	if record.Plan == nil {
		return WorkRecord{}, ErrPlanRequired
	}
	if err := validatePlanBindingForWork(record, *record.Plan); err != nil {
		return WorkRecord{}, err
	}
	signed, err := client.Request(ctx, governance.AdmissionRequest{
		Subject: governance.Subject{
			AgentID: string(record.Event.AgentID), EventID: record.Event.ID,
			WorkerID: lease.WorkerID, LeaseEpoch: lease.Epoch,
			PlanDigest: hex.EncodeToString(record.Plan.Digest),
		},
		Plan: append([]byte(nil), record.Plan.Document...),
	})
	if err != nil {
		return WorkRecord{}, err
	}
	return store.BeginExecution(ctx, lease, signed, now)
}
