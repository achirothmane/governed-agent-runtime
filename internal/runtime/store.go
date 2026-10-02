package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

type Event struct {
	ID        string          `json:"id"`
	AgentID   agent.AgentID   `json:"agent_id"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func (e Event) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("event id is required")
	}
	if strings.TrimSpace(string(e.AgentID)) == "" {
		return errors.New("agent id is required")
	}
	if strings.TrimSpace(e.Kind) == "" {
		return errors.New("event kind is required")
	}
	if e.CreatedAt.IsZero() {
		return errors.New("event created_at is required")
	}
	return nil
}

func sameEvent(a, b Event) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(encodedA, encodedB)
}

type WorkState string

const (
	WorkPending WorkState = "PENDING"
	WorkLeased  WorkState = "LEASED"
	WorkDone    WorkState = "DONE"
)

type WorkRecord struct {
	Sequence           uint64      `json:"sequence"`
	Event              Event       `json:"event"`
	State              WorkState   `json:"state"`
	LifecycleState     agent.State `json:"lifecycle_state"`
	LifecycleVersion   uint64      `json:"lifecycle_version"`
	LifecycleUpdatedAt time.Time   `json:"lifecycle_updated_at"`
	LeaseOwner         string      `json:"lease_owner,omitempty"`
	LeaseEpoch         uint64      `json:"lease_epoch"`
	LeaseExpiresAt     time.Time   `json:"lease_expires_at,omitempty"`
	CompletedAt        *time.Time  `json:"completed_at,omitempty"`
}

type LeaseToken struct {
	EventID   string
	WorkerID  string
	Epoch     uint64
	ExpiresAt time.Time
}

type ClaimedWork struct {
	Record WorkRecord
	Lease  LeaseToken
}

var (
	ErrNoWork                     = errors.New("no claimable work")
	ErrWorkNotFound               = errors.New("work not found")
	ErrLeaseLost                  = errors.New("execution lease lost")
	ErrEventConflict              = errors.New("event id already exists with different content")
	ErrInvalidLifecycleTransition = errors.New("invalid agent lifecycle transition")
	ErrLifecycleIncomplete        = errors.New("agent lifecycle is not complete")
)

type Store interface {
	Enqueue(context.Context, Event) (WorkRecord, error)
	Claim(context.Context, string, time.Time, time.Duration) (ClaimedWork, error)
	Renew(context.Context, LeaseToken, time.Time, time.Duration) (LeaseToken, error)
	Transition(context.Context, LeaseToken, agent.State, time.Time) (WorkRecord, error)
	Complete(context.Context, LeaseToken, time.Time) (WorkRecord, error)
	Get(context.Context, string) (WorkRecord, error)
}

func validateWorkerAndTTL(workerID string, ttl time.Duration) error {
	if strings.TrimSpace(workerID) == "" {
		return errors.New("worker id is required")
	}
	if ttl <= 0 {
		return fmt.Errorf("lease ttl must be positive: %s", ttl)
	}
	return nil
}

func leaseMatches(r WorkRecord, lease LeaseToken, now time.Time) bool {
	return r.State == WorkLeased &&
		r.Event.ID == lease.EventID &&
		r.LeaseOwner == lease.WorkerID &&
		r.LeaseEpoch == lease.Epoch &&
		now.Before(r.LeaseExpiresAt)
}

func initializeLifecycle(r *WorkRecord, at time.Time) {
	if r.LifecycleState != "" {
		return
	}
	r.LifecycleState = agent.StateSleeping
	r.LifecycleVersion = 1
	r.LifecycleUpdatedAt = at
}
