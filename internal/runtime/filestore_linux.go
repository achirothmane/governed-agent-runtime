package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
 "github.com/achirothmane/governed-agent-runtime/internal/governance"
)

type fileState struct {
	NextSequence uint64                `json:"next_sequence"`
	Work         map[string]WorkRecord `json:"work"`
}

type FileStore struct {
	path     string
	lockPath string
}

func NewFileStore(path string) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store path is required")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}

	return &FileStore{path: path, lockPath: path + ".lock"}, nil
}

func (s *FileStore) Enqueue(ctx context.Context, event Event) (WorkRecord, error) {
	if err := event.Validate(); err != nil {
		return WorkRecord{}, err
	}

	var out WorkRecord
	err := s.withLockedState(ctx, func(state *fileState) (bool, error) {
		if existing, ok := state.Work[event.ID]; ok {
			if !sameEvent(existing.Event, event) {
				return false, ErrEventConflict
			}
			initializeLifecycle(&existing, existing.Event.CreatedAt)
			out = existing
			return false, nil
		}

		state.NextSequence++
		out = WorkRecord{
			Sequence:           state.NextSequence,
			Event:              event,
			State:              WorkPending,
			LifecycleState:     agent.StateSleeping,
			LifecycleVersion:   1,
			LifecycleUpdatedAt: event.CreatedAt,
		}
		state.Work[event.ID] = out
		return true, nil
	})
	return out, err
}

func (s *FileStore) Claim(ctx context.Context, workerID string, now time.Time, ttl time.Duration) (ClaimedWork, error) {
	if err := validateWorkerAndTTL(workerID, ttl); err != nil {
		return ClaimedWork{}, err
	}

	var out ClaimedWork
	err := s.withLockedState(ctx, func(state *fileState) (bool, error) {
		eligible := make([]WorkRecord, 0)
		for _, r := range state.Work {
			if r.State == WorkPending || (r.State == WorkLeased && !r.LeaseExpiresAt.After(now)) {
				eligible = append(eligible, r)
			}
		}
		if len(eligible) == 0 {
			return false, ErrNoWork
		}

		sort.Slice(eligible, func(i, j int) bool {
			return eligible[i].Sequence < eligible[j].Sequence
		})

		r := eligible[0]
		initializeLifecycle(&r, r.Event.CreatedAt)

		takingOverExpiredLease := r.State == WorkLeased && !r.LeaseExpiresAt.After(now)
		if takingOverExpiredLease && r.LifecycleState == agent.StateExecuting {
			r.LifecycleState = agent.StateUnknown
			r.LifecycleVersion++
			r.LifecycleUpdatedAt = now
		}

		r.State = WorkLeased
		r.LeaseOwner = workerID
		r.LeaseEpoch++
		r.LeaseExpiresAt = now.Add(ttl)
		r.CompletedAt = nil
		state.Work[r.Event.ID] = r

		out = ClaimedWork{
			Record: r,
			Lease: LeaseToken{
				EventID:   r.Event.ID,
				WorkerID:  workerID,
				Epoch:     r.LeaseEpoch,
				ExpiresAt: r.LeaseExpiresAt,
			},
		}
		return true, nil
	})
	return out, err
}

func (s *FileStore) Renew(ctx context.Context, lease LeaseToken, now time.Time, ttl time.Duration) (LeaseToken, error) {
	if err := validateWorkerAndTTL(lease.WorkerID, ttl); err != nil {
		return LeaseToken{}, err
	}

	var out LeaseToken
	err := s.withLockedState(ctx, func(state *fileState) (bool, error) {
		r, ok := state.Work[lease.EventID]
		if !ok {
			return false, ErrWorkNotFound
		}
		if !leaseMatches(r, lease, now) {
			return false, ErrLeaseLost
		}

		r.LeaseExpiresAt = now.Add(ttl)
		state.Work[r.Event.ID] = r
		out = lease
		out.ExpiresAt = r.LeaseExpiresAt
		return true, nil
	})
	return out, err
}

func (s *FileStore) BindPlan(ctx context.Context, lease LeaseToken, binding PlanBinding, now time.Time) (WorkRecord, error) {
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
		if r.LifecycleState != agent.StatePlanning {
			return false, fmt.Errorf("%w: plan may only be bound while PLANNING, state=%s", ErrPlanBindingInvalid, r.LifecycleState)
		}
		if err := validatePlanBindingForWork(r, binding); err != nil {
			return false, err
		}
		if r.Plan != nil {
			if samePlanBinding(*r.Plan, binding) {
				out = r
				return false, nil
			}
			return false, ErrPlanConflict
		}

		cloned := binding.Clone()
		boundAt := now
		r.Plan = &cloned
		r.PlanBoundAt = &boundAt
		state.Work[r.Event.ID] = r
		out = r
		return true, nil
	})
	return out, err
}

func (s *FileStore) Transition(ctx context.Context, lease LeaseToken, target agent.State, now time.Time) (WorkRecord, error) {
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
		if !agent.CanTransition(r.LifecycleState, target) {
			return false, fmt.Errorf("%w: %s -> %s", ErrInvalidLifecycleTransition, r.LifecycleState, target)
		}
		if target == agent.StateExecuting {
 return false, governance.ErrAdmission
 }
 if target == agent.StateWaitingForAdmission && r.Plan == nil {
			return false, ErrPlanRequired
		}

		r.LifecycleState = target
		r.LifecycleVersion++
		r.LifecycleUpdatedAt = now
		state.Work[r.Event.ID] = r
		out = r
		return true, nil
	})
	return out, err
}

func (s *FileStore) Complete(ctx context.Context, lease LeaseToken, now time.Time) (WorkRecord, error) {
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
		if r.LifecycleState != agent.StateSleeping {
			return false, fmt.Errorf("%w: state=%s", ErrLifecycleIncomplete, r.LifecycleState)
		}

		r.State = WorkDone
		completedAt := now
		r.CompletedAt = &completedAt
		state.Work[r.Event.ID] = r
		out = r
		return true, nil
	})
	return out, err
}

func (s *FileStore) Get(ctx context.Context, eventID string) (WorkRecord, error) {
	var out WorkRecord
	err := s.withLockedState(ctx, func(state *fileState) (bool, error) {
		r, ok := state.Work[eventID]
		if !ok {
			return false, ErrWorkNotFound
		}
		initializeLifecycle(&r, r.Event.CreatedAt)
		out = r
		return false, nil
	})
	return out, err
}

func (s *FileStore) withLockedState(ctx context.Context, fn func(*fileState) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	lock, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open store lock: %w", err)
	}
	defer lock.Close()

	if err := lockFile(ctx, lock); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	state, err := s.loadLocked()
	if err != nil {
		return err
	}

	changed, err := fn(state)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return s.persistLocked(state)
}

func lockFile(ctx context.Context, file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			return fmt.Errorf("lock store: %w", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (s *FileStore) loadLocked() (*fileState, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &fileState{Work: make(map[string]WorkRecord)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read durable store: %w", err)
	}

	var state fileState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode durable store: %w", err)
	}
	if state.Work == nil {
		state.Work = make(map[string]WorkRecord)
	}
	return &state, nil
}

func (s *FileStore) persistLocked(state *fileState) error {
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".agent-runtime-*.tmp")
	if err != nil {
		return fmt.Errorf("create durable store temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod durable store temp file: %w", err)
	}

	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		tmp.Close()
		return fmt.Errorf("encode durable store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync durable store temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close durable store temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace durable store: %w", err)
	}

	dirHandle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open durable store directory: %w", err)
	}
	defer dirHandle.Close()
	if err := dirHandle.Sync(); err != nil {
		return fmt.Errorf("sync durable store directory: %w", err)
	}
	return nil
}


func (s *FileStore) BeginExecution(ctx context.Context, lease LeaseToken, signed governance.SignedAdmission, verifier governance.Verifier, now time.Time) (WorkRecord, error) {
 signed = governance.SignedAdmission{Document: append([]byte(nil), signed.Document...), Signature: append([]byte(nil), signed.Signature...)}
 var out WorkRecord
 err := s.withLockedState(ctx, func(state *fileState) (bool, error) {
  r, ok := state.Work[lease.EventID]
  if !ok { return false, ErrWorkNotFound }
  if err := verifyAdmission(ctx, r, lease, signed, verifier, now); err != nil { return false, err }
  r.Admission = &governance.SignedAdmission{Document: append([]byte(nil), signed.Document...), Signature: append([]byte(nil), signed.Signature...)}
  r.LifecycleState = agent.StateExecuting
  r.LifecycleVersion++
  r.LifecycleUpdatedAt = now
  state.Work[r.Event.ID] = r
  out = r
  return true, nil
 })
 return out, err
}
