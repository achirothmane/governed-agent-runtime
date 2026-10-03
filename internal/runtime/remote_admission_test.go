package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
	"github.com/achirothmane/governed-agent-runtime/internal/governance"
)

func prepareRemoteWork(t *testing.T, store Store, now time.Time) ClaimedWork {
	t.Helper()
	ctx := context.Background()
	if _, err := store.Enqueue(ctx, testEvent("remote-event", now)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "remote-worker", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []agent.State{agent.StateWaking, agent.StatePlanning} {
		if _, err := store.Transition(ctx, claim.Lease, state, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BindPlan(ctx, claim.Lease, testPlanBinding(claim.Lease.EventID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claim.Lease, agent.StateWaitingForAdmission, now); err != nil {
		t.Fatal(err)
	}
	return claim
}

func testRemoteAdmission(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	claim := prepareRemoteWork(t, store, now)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	witness := governance.Witness{EvidenceDigest: "ci-evidence", AuthorityRef: "host-authority", PolicyHash: "host-policy", DoctrineEpoch: 1}
	var revoked atomic.Bool
	binding := testPlanBinding(claim.Lease.EventID)
	allowedDigest := hex.EncodeToString(binding.Digest)
	config := governance.ServiceConfig{
		PrivateKey: privateKey, BearerToken: "integration-test-credential",
		Clock: func() time.Time { return now }, MaxAdmissions: 10,
		Authorize: func(_ context.Context, request governance.AdmissionRequest) (governance.Grant, error) {
			if request.Subject.PlanDigest != allowedDigest || revoked.Load() {
				return governance.Grant{}, governance.ErrAdmission
			}
			return governance.Grant{Target: "test-repository", Profile: "release-engineer", Witness: witness, ValidUntil: now.Add(30*time.Second)}, nil
		},
		Revalidate: func(_ context.Context, _ governance.Admission) (governance.Witness, error) {
			if revoked.Load() {
				return governance.Witness{}, governance.ErrAdmission
			}
			return witness, nil
		},
	}
	service, err := governance.NewService(config)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service)
	defer server.Close()
	client, err := governance.NewClient(server.URL, config.BearerToken)
	if err != nil {
		t.Fatal(err)
	}
	configureTestVerifier(store, client.Verifier(publicKey, config.Clock))
	record, err := store.Get(ctx, claim.Lease.EventID)
	if err != nil {
		t.Fatal(err)
	}
	request := governance.AdmissionRequest{
		Subject: governance.Subject{AgentID: string(record.Event.AgentID), EventID: record.Event.ID, WorkerID: claim.Lease.WorkerID, LeaseEpoch: claim.Lease.Epoch, PlanDigest: allowedDigest},
		Plan: record.Plan.Document,
	}
	signed, err := client.Request(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	if _, err := store.BeginExecution(ctx, claim.Lease, signed, now); err == nil {
		t.Fatal("authority revoked after issue was accepted")
	}
	record, err = store.Get(ctx, claim.Lease.EventID)
	if err != nil || record.LifecycleState != agent.StateWaitingForAdmission || record.Admission != nil {
		t.Fatalf("remote denial changed durable state: %+v %v", record, err)
	}
	revoked.Store(false)
	restarted, err := governance.NewService(config)
	if err != nil {
		t.Fatal(err)
	}
	restartedServer := httptest.NewServer(restarted)
	defer restartedServer.Close()
	restartedClient, err := governance.NewClient(restartedServer.URL, config.BearerToken)
	if err != nil {
		t.Fatal(err)
	}
	configureTestVerifier(store, restartedClient.Verifier(publicKey, config.Clock))
	if _, err := store.BeginExecution(ctx, claim.Lease, signed, now); err == nil {
		t.Fatal("pre-restart issuance accepted by empty issuer ledger")
	}
	configureTestVerifier(store, client.Verifier(publicKey, config.Clock))
	badClient, err := governance.NewClient(server.URL, "wrong-credential")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BeginRemoteExecution(ctx, store, claim.Lease, badClient, now); err == nil {
		t.Fatal("unauthenticated admission issue accepted")
	}
	record, err = BeginRemoteExecution(ctx, store, claim.Lease, client, now)
	if err != nil || record.LifecycleState != agent.StateExecuting || record.Admission == nil {
		t.Fatalf("live HTTP admission failed: %+v %v", record, err)
	}
}

func TestFileStoreLiveRemoteAdmission(t *testing.T) {
	testRemoteAdmission(t, newTestStore(t, filepath.Join(t.TempDir(), "remote.json")))
}

func TestPostgresLiveRemoteAdmission(t *testing.T) {
	testRemoteAdmission(t, newPostgresTestStore(t))
}
