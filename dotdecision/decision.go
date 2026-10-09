package dotdecision

import (
	"errors"
	"fmt"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

type Kind string

const (
	ProposeNextGate Kind = "PROPOSE_NEXT_GATE"
	AskHuman        Kind = "ASK_HUMAN"
	Refuse          Kind = "REFUSE"
)

var (
	ErrInvalidDecision   = errors.New("invalid Dot decision")
	ErrSnapshotMismatch  = errors.New("Dot decision snapshot mismatch")
	ErrWorkItemUnknown   = errors.New("Dot decision references unknown work item")
	ErrAuthorityExceeded = errors.New("Dot decision exceeds admitted authority")
	ErrHumanRequired     = errors.New("Dot decision requires human authority")
)

type Decision struct {
	Kind               Kind   `json:"kind"`
	SnapshotDigest     string `json:"snapshot_digest"`
	WorkItemID         string `json:"work_item_id"`
	RequestedAuthority string `json:"requested_authority"`
	RequestedAction    string `json:"requested_action,omitempty"`
	Rationale          string `json:"rationale"`
}

func Validate(snapshot portfoliocontext.Snapshot, decision Decision) error {
	if strings.TrimSpace(decision.SnapshotDigest) == "" ||
		strings.TrimSpace(decision.WorkItemID) == "" ||
		strings.TrimSpace(decision.RequestedAuthority) == "" ||
		strings.TrimSpace(decision.Rationale) == "" {
		return ErrInvalidDecision
	}
	switch decision.Kind {
	case ProposeNextGate, AskHuman, Refuse:
	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidDecision, decision.Kind)
	}
	if decision.SnapshotDigest != snapshot.SnapshotDigest {
		return fmt.Errorf("%w: decision=%s current=%s", ErrSnapshotMismatch, decision.SnapshotDigest, snapshot.SnapshotDigest)
	}

	var item *portfoliocontext.RunnableItem
	for i := range snapshot.Projection.RunnableItems {
		if snapshot.Projection.RunnableItems[i].ID == decision.WorkItemID {
			item = &snapshot.Projection.RunnableItems[i]
			break
		}
	}
	if item == nil {
		return fmt.Errorf("%w: %s", ErrWorkItemUnknown, decision.WorkItemID)
	}

	admitted, ok := authorityRank(item.Authority)
	if !ok {
		return fmt.Errorf("%w: admitted authority %q is unknown", ErrInvalidDecision, item.Authority)
	}
	requested, ok := authorityRank(decision.RequestedAuthority)
	if !ok {
		return fmt.Errorf("%w: requested authority %q is unknown", ErrInvalidDecision, decision.RequestedAuthority)
	}
	if requested > admitted {
		return fmt.Errorf("%w: requested=%s admitted=%s", ErrAuthorityExceeded, decision.RequestedAuthority, item.Authority)
	}

	action := strings.TrimSpace(decision.RequestedAction)
	if action != "" && snapshot.RequiresHuman(action) {
		if decision.Kind != AskHuman {
			return fmt.Errorf("%w: action=%s", ErrHumanRequired, action)
		}
	} else if decision.Kind == AskHuman {
		return fmt.Errorf("%w: ASK_HUMAN requires a registered human-final action", ErrInvalidDecision)
	}

	return nil
}

func authorityRank(value string) (int, bool) {
	switch strings.TrimSpace(value) {
	case "OBSERVE":
		return 0, true
	case "PREPARE":
		return 1, true
	case "EXECUTE_REVERSIBLE":
		return 2, true
	case "COMMIT_EXTERNAL":
		return 3, true
	default:
		return 0, false
	}
}
