package dotdurable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

var (
	ErrDecisionNotFound = errors.New("durable Dot decision not found")
	ErrDecisionConflict = errors.New("durable Dot decision binding conflict")
	ErrSnapshotMismatch = errors.New("durable Dot snapshot mismatch")
)

type Request struct {
	DecisionID     string `json:"decision_id"`
	SnapshotDigest string `json:"snapshot_digest"`
}

func (r Request) Validate() error {
	if strings.TrimSpace(r.DecisionID) == "" {
		return errors.New("decision id is required")
	}
	if len(r.SnapshotDigest) != 64 {
		return errors.New("snapshot digest must be SHA-256 hex")
	}
	for _, ch := range r.SnapshotDigest {
		if !strings.ContainsRune("0123456789abcdef", ch) {
			return errors.New("snapshot digest must be lowercase SHA-256 hex")
		}
	}
	return nil
}

type Record struct {
	DecisionID         string           `json:"decision_id"`
	SnapshotDigest     string           `json:"snapshot_digest"`
	WorkItemID         string           `json:"work_item_id"`
	Kind               dotdecision.Kind `json:"kind"`
	RequestedAuthority string           `json:"requested_authority"`
	RequestedAction    string           `json:"requested_action,omitempty"`
	Rationale          string           `json:"rationale"`
	DecisionDigest     string           `json:"decision_digest"`
}

func (r Record) Validate() error {
	if err := (Request{DecisionID: r.DecisionID, SnapshotDigest: r.SnapshotDigest}).Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorkItemID) == "" ||
		strings.TrimSpace(r.RequestedAuthority) == "" ||
		strings.TrimSpace(r.Rationale) == "" {
		return errors.New("durable Dot decision binding is incomplete")
	}
	switch r.Kind {
	case dotdecision.ProposeNextGate, dotdecision.AskHuman, dotdecision.Refuse:
	default:
		return fmt.Errorf("unsupported durable Dot decision kind %q", r.Kind)
	}
	if len(r.DecisionDigest) != 64 {
		return errors.New("decision digest must be SHA-256 hex")
	}
	return nil
}

type SnapshotLoader interface {
	LoadSnapshot(context.Context, string) (portfoliocontext.Snapshot, error)
}

type Reasoner interface {
	Decide(context.Context, portfoliocontext.ReasoningView) (dotdecision.Decision, error)
}

type Store interface {
	GetCommittedDecision(context.Context, string) (Record, error)
	PutCommittedDecision(context.Context, Record) (Record, bool, error)
}

func DecisionDigest(decision dotdecision.Decision) (string, error) {
	raw, err := json.Marshal(decision)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func SameRecord(a, b Record) bool {
	return a == b
}
