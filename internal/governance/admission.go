// Package governance verifies externally issued admission. It never issues authority.
package governance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/achirothmane/aegis-ege/governedaction"
)

var ErrAdmission = errors.New("admission is missing, stale or does not match execution")

// Witness must be obtained from trusted current governance state, outside the model.
type Witness struct {
	EvidenceDigest string
	AuthorityRef   string
	PolicyHash     string
	DoctrineEpoch  uint64
}

type Admission struct {
	ID         string
	AgentID    string
	EventID    string
	WorkerID   string
	LeaseEpoch uint64
	PlanDigest string
	Target     string
	Profile    string
	Witness    Witness
	IssuedAt   time.Time
	ValidUntil time.Time
}

// SignedAdmission preserves exact signed bytes; no JSON reserialization is used.
type SignedAdmission struct {
	Document  []byte
	Signature []byte
}

type Subject struct {
	AgentID    string
	EventID    string
	WorkerID   string
	LeaseEpoch uint64
	PlanDigest string
}

type Verifier struct {
	PublicKey ed25519.PublicKey
	Clock     func() time.Time
	Current   func(context.Context, Admission) (Witness, error)
}

func (v Verifier) Verify(ctx context.Context, signed SignedAdmission, subject Subject, now time.Time) (Admission, error) {
	if err := ctx.Err(); err != nil {
		return Admission{}, err
	}
	if len(v.PublicKey) != ed25519.PublicKeySize || v.Current == nil || v.Clock == nil ||
		!ed25519.Verify(v.PublicKey, signed.Document, signed.Signature) {
		return Admission{}, ErrAdmission
	}
	now = v.Clock()
	var a Admission
	decoder := json.NewDecoder(bytes.NewReader(signed.Document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&a) != nil {
		return Admission{}, ErrAdmission
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Admission{}, ErrAdmission
	}
	if a.ID == "" || a.AgentID == "" || a.EventID == "" || a.WorkerID == "" ||
		a.LeaseEpoch == 0 || a.AgentID != subject.AgentID || a.EventID != subject.EventID ||
		a.WorkerID != subject.WorkerID || a.LeaseEpoch != subject.LeaseEpoch ||
		a.IssuedAt.IsZero() || now.Before(a.IssuedAt) ||
		!a.IssuedAt.Before(a.ValidUntil) || governedaction.CheckValidity(a.ValidUntil, now) != nil {
		return Admission{}, ErrAdmission
	}
	digest, err := hex.DecodeString(a.PlanDigest)
	if err != nil || len(digest) != sha256.Size {
		return Admission{}, ErrAdmission
	}
	if governedaction.CheckBinding(
		governedaction.Binding{ActionRevision: a.PlanDigest, Target: a.Target, Profile: a.Profile},
		governedaction.Binding{ActionRevision: subject.PlanDigest, Target: a.Target, Profile: a.Profile},
	) != nil {
		return Admission{}, ErrAdmission
	}
	if a.Witness.EvidenceDigest == "" || a.Witness.AuthorityRef == "" ||
		a.Witness.PolicyHash == "" || a.Witness.DoctrineEpoch == 0 {
		return Admission{}, ErrAdmission
	}
	current, err := v.Current(ctx, a)
	if err != nil {
		return Admission{}, err
	}
	if current != a.Witness || governedaction.CheckValidity(a.ValidUntil, v.Clock()) != nil {
		return Admission{}, ErrAdmission
	}
	return a, ctx.Err()
}

func (v Verifier) Clone() Verifier {
	v.PublicKey = append(ed25519.PublicKey(nil), v.PublicKey...)
	return v
}
