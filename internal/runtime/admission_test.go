package runtime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
	"github.com/achirothmane/governed-agent-runtime/internal/governance"
)

func admissionFixture(t *testing.T, s Store, lease LeaseToken, now time.Time) (governance.SignedAdmission, governance.Verifier, governance.Admission, ed25519.PrivateKey) {
	t.Helper()
	r, err := s.Get(context.Background(), lease.EventID)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := governance.Admission{
		ID: "admission-1", AgentID: string(r.Event.AgentID), EventID: lease.EventID,
		WorkerID: lease.WorkerID, LeaseEpoch: lease.Epoch, PlanDigest: hex.EncodeToString(r.Plan.Digest),
		Target: "github:account/repository", Profile: "release-engineer-v1",
		Witness:  governance.Witness{EvidenceDigest: "evidence-1", AuthorityRef: "authority-1", PolicyHash: "policy-1", DoctrineEpoch: 1},
		IssuedAt: now, ValidUntil: now.Add(time.Minute),
	}
	signed := signAdmission(t, a, key)
	v := governance.Verifier{PublicKey: pub, Clock: func() time.Time { return now }, Current: func(context.Context, governance.Admission) (governance.Witness, error) {
		return a.Witness, nil
	}}
	configureTestVerifier(s, v)
	return signed, v, a, key
}

func signAdmission(t *testing.T, a governance.Admission, key ed25519.PrivateKey) governance.SignedAdmission {
	t.Helper()
	doc, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return governance.SignedAdmission{Document: doc, Signature: ed25519.Sign(key, doc)}
}

func beginTestExecution(t *testing.T, s Store, lease LeaseToken, now time.Time) (WorkRecord, error) {
	t.Helper()
	signed, _, _, _ := admissionFixture(t, s, lease, now)
	return s.BeginExecution(context.Background(), lease, signed, now)
}

func testAdmissionStore(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	if _, err := s.Enqueue(ctx, testEvent("admission-event", now)); err != nil {
		t.Fatal(err)
	}
	claim, err := s.Claim(ctx, "worker-a", now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []agent.State{agent.StateWaking, agent.StatePlanning} {
		if _, err := s.Transition(ctx, claim.Lease, state, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.BindPlan(ctx, claim.Lease, testPlanBinding(claim.Lease.EventID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, claim.Lease, agent.StateWaitingForAdmission, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, claim.Lease, agent.StateExecuting, now); !errors.Is(err, governance.ErrAdmission) {
		t.Fatalf("direct execution: %v", err)
	}
	signed, v, a, key := admissionFixture(t, s, claim.Lease, now)
	cases := []struct {
		name   string
		change func(*governance.Admission)
	}{
		{"plan", func(a *governance.Admission) { a.PlanDigest = hex.EncodeToString(make([]byte, 32)) }},
		{"agent", func(a *governance.Admission) { a.AgentID = "other" }},
		{"event", func(a *governance.Admission) { a.EventID = "other" }},
		{"worker", func(a *governance.Admission) { a.WorkerID = "other" }},
		{"epoch", func(a *governance.Admission) { a.LeaseEpoch++ }},
		{"expired", func(a *governance.Admission) { a.ValidUntil = now }},
		{"future", func(a *governance.Admission) { a.IssuedAt = now.Add(time.Second) }},
		{"policy", func(a *governance.Admission) { a.Witness.PolicyHash = "changed" }},
		{"evidence", func(a *governance.Admission) { a.Witness.EvidenceDigest = "changed" }},
		{"authority", func(a *governance.Admission) { a.Witness.AuthorityRef = "revoked" }},
		{"doctrine", func(a *governance.Admission) { a.Witness.DoctrineEpoch++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := a
			tc.change(&bad)
			if _, err := s.BeginExecution(ctx, claim.Lease, signAdmission(t, bad, key), now); err == nil {
				t.Fatal("invalid admission accepted")
			}
			r, err := s.Get(ctx, claim.Lease.EventID)
			if err != nil || r.LifecycleState != agent.StateWaitingForAdmission || r.Admission != nil {
				t.Fatalf("rejected admission mutated state: %+v %v", r, err)
			}
		})
	}
	badSignature := governance.SignedAdmission{Document: signed.Document, Signature: make([]byte, ed25519.SignatureSize)}
	if _, err := s.BeginExecution(ctx, claim.Lease, badSignature, now); err == nil {
		t.Fatal("bad signature accepted")
	}

	slow := v
	boundaryTime := now
	slow.Clock = func() time.Time { return boundaryTime }
	slow.Current = func(context.Context, governance.Admission) (governance.Witness, error) {
		boundaryTime = now.Add(2 * time.Minute)
		return a.Witness, nil
	}
	configureTestVerifier(s, slow)
	if _, err := s.BeginExecution(ctx, claim.Lease, signed, now); err == nil {
		t.Fatal("admission expired during current-state lookup accepted")
	}
	configureTestVerifier(s, v)
	r, err := s.BeginExecution(ctx, claim.Lease, signed, now)
	if err != nil || r.LifecycleState != agent.StateExecuting || r.Admission == nil {
		t.Fatalf("valid admission rejected: %+v %v", r, err)
	}

	var reopened Store
	switch concrete := s.(type) {
	case *FileStore:
		reopened, err = NewFileStoreWithGovernance(concrete.path, v)
	case *PostgresStore:
		reopened, err = NewPostgresStoreWithGovernance(concrete.db, v)
	}
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Get(ctx, claim.Lease.EventID)
	if err != nil || persisted.Admission == nil || !bytes.Equal(persisted.Admission.Document, signed.Document) || !bytes.Equal(persisted.Admission.Signature, signed.Signature) {
		t.Fatalf("exact admission lost after reopen: %+v %v", persisted, err)
	}
	if _, err := s.BeginExecution(ctx, claim.Lease, signed, now); err == nil {
		t.Fatal("execution admission replay accepted")
	}
	takeover, err := s.Claim(ctx, "worker-b", now.Add(3*time.Minute), time.Minute)
	if err != nil || takeover.Record.LifecycleState != agent.StateUnknown {
		t.Fatalf("takeover must preserve uncertainty: %+v %v", takeover, err)
	}
	if _, err := s.BeginExecution(ctx, claim.Lease, signed, now.Add(3*time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old worker admission: %v", err)
	}
}

func TestFileStoreAdmission(t *testing.T) {
	testAdmissionStore(t, newTestStore(t, filepath.Join(t.TempDir(), "runtime.json")))
}

func TestPostgresStoreAdmission(t *testing.T) {
	testAdmissionStore(t, newPostgresTestStore(t))
}

// Test-only fixture setup; production callers configure immutable store options.
func configureTestVerifier(s Store, v governance.Verifier) {
	switch concrete := s.(type) {
	case *FileStore:
		concrete.verifier = v.Clone()
	case *PostgresStore:
		concrete.verifier = v.Clone()
	}
}
