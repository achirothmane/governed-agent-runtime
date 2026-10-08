package runtime

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

type EffectResolutionOutcome string

const (
	EffectAppliedOnce EffectResolutionOutcome = "APPLIED_ONCE"
	EffectAbsent      EffectResolutionOutcome = "ABSENT"
)

var ErrEffectResolution = errors.New("invalid effect reconciliation proof")

// EffectResolution is a durable proof used to leave UNKNOWN.
//
// It is deliberately bound to the current worker/lease epoch and the exact
// immutable plan digest. A stale worker cannot replay an old ABSENT proof to
// obtain new execution authority.
type EffectResolution struct {
	EffectID       string                  `json:"effect_id"`
	Outcome        EffectResolutionOutcome `json:"outcome"`
	WorkerID       string                  `json:"worker_id"`
	LeaseEpoch     uint64                  `json:"lease_epoch"`
	PlanDigest     string                  `json:"plan_digest"`
	EvidenceDigest string                  `json:"evidence_digest"`
	ObservedAt     time.Time               `json:"observed_at"`
}

func (r EffectResolution) Validate() error {
	if strings.TrimSpace(r.EffectID) == "" ||
		strings.TrimSpace(r.WorkerID) == "" ||
		r.LeaseEpoch == 0 ||
		r.ObservedAt.IsZero() {
		return ErrEffectResolution
	}
	switch r.Outcome {
	case EffectAppliedOnce, EffectAbsent:
	default:
		return fmt.Errorf("%w: unsupported outcome %q", ErrEffectResolution, r.Outcome)
	}
	for name, digest := range map[string]string{
		"plan":     r.PlanDigest,
		"evidence": r.EvidenceDigest,
	} {
		if len(digest) != 64 {
			return fmt.Errorf("%w: %s digest must be SHA-256 hex", ErrEffectResolution, name)
		}
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("%w: %s digest must be SHA-256 hex", ErrEffectResolution, name)
		}
	}
	return nil
}

func (r EffectResolution) targetState() agent.State {
	if r.Outcome == EffectAppliedOnce {
		return agent.StateVerifying
	}
	return agent.StateWaitingForAdmission
}
