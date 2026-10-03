package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
 "github.com/achirothmane/governed-agent-runtime/internal/governance"
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

type PlanBinding struct {
	PlanID   string        `json:"plan_id"`
	AgentID  agent.AgentID `json:"agent_id"`
	EventID  string        `json:"event_id"`
	Digest   []byte        `json:"digest"`
	Document []byte        `json:"document"`
}

func (b PlanBinding) Clone() PlanBinding {
	out := b
	out.Digest = append([]byte(nil), b.Digest...)
	out.Document = append([]byte(nil), b.Document...)
	return out
}

func (b PlanBinding) Validate() error {
	if strings.TrimSpace(b.PlanID) == "" {
		return errors.New("plan binding id is required")
	}
	if strings.TrimSpace(string(b.AgentID)) == "" {
		return errors.New("plan binding agent id is required")
	}
	if strings.TrimSpace(b.EventID) == "" {
		return errors.New("plan binding event id is required")
	}
	if len(b.Digest) != sha256.Size {
		return fmt.Errorf("plan binding digest must be %d bytes, got %d", sha256.Size, len(b.Digest))
	}
	if len(b.Document) == 0 || !json.Valid(b.Document) {
		return errors.New("plan binding document must be valid JSON")
	}
	sum := sha256.Sum256(b.Document)
	if !bytes.Equal(b.Digest, sum[:]) {
		return errors.New("plan binding digest does not match document")
	}
	return nil
}

type WorkRecord struct {
 Admission *governance.SignedAdmission `json:"admission,omitempty"`
	Sequence           uint64       `json:"sequence"`
	Event              Event        `json:"event"`
	State              WorkState    `json:"state"`
	LifecycleState     agent.State  `json:"lifecycle_state"`
	LifecycleVersion   uint64       `json:"lifecycle_version"`
	LifecycleUpdatedAt time.Time    `json:"lifecycle_updated_at"`
	Plan               *PlanBinding `json:"plan,omitempty"`
	PlanBoundAt        *time.Time   `json:"plan_bound_at,omitempty"`
	LeaseOwner         string       `json:"lease_owner,omitempty"`
	LeaseEpoch         uint64       `json:"lease_epoch"`
	LeaseExpiresAt     time.Time    `json:"lease_expires_at,omitempty"`
	CompletedAt        *time.Time   `json:"completed_at,omitempty"`
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
	ErrPlanRequired               = errors.New("durable plan binding is required")
	ErrPlanConflict               = errors.New("different plan is already bound to this work")
	ErrPlanBindingInvalid         = errors.New("plan binding does not match work identity")
)

type Store interface {
 BeginExecution(context.Context, LeaseToken, governance.SignedAdmission, governance.Verifier, time.Time) (WorkRecord, error)
	Enqueue(context.Context, Event) (WorkRecord, error)
	Claim(context.Context, string, time.Time, time.Duration) (ClaimedWork, error)
	Renew(context.Context, LeaseToken, time.Time, time.Duration) (LeaseToken, error)
	BindPlan(context.Context, LeaseToken, PlanBinding, time.Time) (WorkRecord, error)
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

func validatePlanBindingForWork(record WorkRecord, binding PlanBinding) error {
	if err := binding.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrPlanBindingInvalid, err)
	}
	if binding.AgentID != record.Event.AgentID {
		return fmt.Errorf("%w: plan agent %q does not match event agent %q", ErrPlanBindingInvalid, binding.AgentID, record.Event.AgentID)
	}
	if binding.EventID != record.Event.ID {
		return fmt.Errorf("%w: plan event %q does not match work event %q", ErrPlanBindingInvalid, binding.EventID, record.Event.ID)
	}
	return nil
}

func samePlanBinding(a, b PlanBinding) bool {
	return a.PlanID == b.PlanID &&
		a.AgentID == b.AgentID &&
		a.EventID == b.EventID &&
		bytes.Equal(a.Digest, b.Digest) &&
		bytes.Equal(a.Document, b.Document)
}


func verifyAdmission(ctx context.Context, r WorkRecord, lease LeaseToken, signed governance.SignedAdmission, verifier governance.Verifier, now time.Time) error {
 if !leaseMatches(r, lease, now) { return ErrLeaseLost }
 if r.LifecycleState != agent.StateWaitingForAdmission { return ErrInvalidLifecycleTransition }
 if r.Plan == nil { return ErrPlanRequired }
 if err := validatePlanBindingForWork(r, *r.Plan); err != nil { return err }
 _, err := verifier.Verify(ctx, signed, governance.Subject{AgentID: string(r.Event.AgentID), EventID: r.Event.ID, WorkerID: lease.WorkerID, LeaseEpoch: lease.Epoch, PlanDigest: fmt.Sprintf("%x", r.Plan.Digest)}, now)
 return err
}
