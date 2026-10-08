package a7

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
	"github.com/achirothmane/governed-agent-runtime/internal/cognition"
	"github.com/achirothmane/governed-agent-runtime/internal/effects/githubpr"
	"github.com/achirothmane/governed-agent-runtime/internal/governance"
	"github.com/achirothmane/governed-agent-runtime/internal/releaseengineer"
	agentruntime "github.com/achirothmane/governed-agent-runtime/internal/runtime"
)

func TestRealGitHubRecoveryFromPrecreatedEffect(t *testing.T) {
	if os.Getenv("A7_PRECREATED_RECOVERY") != "1" {
		t.Skip("isolated live GitHub recovery proof only")
	}
	token := strings.TrimSpace(os.Getenv("A7_GITHUB_TOKEN"))
	owner := strings.TrimSpace(os.Getenv("A7_OWNER"))
	repo := strings.TrimSpace(os.Getenv("A7_REPO"))
	head := strings.TrimSpace(os.Getenv("A7_PRECREATED_HEAD"))
	headSHA := strings.TrimSpace(os.Getenv("A7_PRECREATED_SHA"))
	eventID := strings.TrimSpace(os.Getenv("A7_PRECREATED_EVENT_ID"))
	if token == "" || owner == "" || repo == "" || head == "" || headSHA == "" || eventID == "" {
		t.Fatal("precreated A7 environment is incomplete")
	}

	ctx := context.Background()
	t0 := time.Date(2026, 10, 8, 6, 55, 0, 0, time.UTC)
	var clockMu sync.RWMutex
	current := t0
	clock := func() time.Time {
		clockMu.RLock()
		defer clockMu.RUnlock()
		return current
	}
	setClock := func(value time.Time) {
		clockMu.Lock()
		current = value
		clockMu.Unlock()
	}

	pub, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	witness := governance.Witness{
		EvidenceDigest: "a7-precreated-live-github",
		AuthorityRef:   "a7-precreated-release-authority",
		PolicyHash:     "a7-precreated-policy",
		DoctrineEpoch:  1,
	}
	var authorizeMu sync.Mutex
	authorizeCount := 0
	service, err := governance.NewService(governance.ServiceConfig{
		PrivateKey:  privateKey,
		BearerToken: "a7-precreated-admission",
		Clock:       clock,
		Authorize: func(_ context.Context, _ governance.AdmissionRequest) (governance.Grant, error) {
			authorizeMu.Lock()
			authorizeCount++
			authorizeMu.Unlock()
			return governance.Grant{
				Target:     "github:" + strings.ToLower(owner+"/"+repo),
				Profile:    releaseengineer.AdmissionProfile,
				Witness:    witness,
				ValidUntil: clock().Add(time.Hour),
			}, nil
		},
		Revalidate: func(context.Context, governance.Admission) (governance.Witness, error) {
			return witness, nil
		},
		MaxAdmissions: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	admissionServer := httptest.NewServer(service)
	defer admissionServer.Close()
	admissionClient, err := governance.NewClient(admissionServer.URL, "a7-precreated-admission")
	if err != nil {
		t.Fatal(err)
	}

	store, err := agentruntime.NewFileStoreWithGovernance(
		t.TempDir()+"/a7-precreated-runtime.json",
		admissionClient.Verifier(pub, clock),
	)
	if err != nil {
		t.Fatal(err)
	}
	event := agentruntime.Event{
		ID:        eventID,
		AgentID:   agent.AgentID("release-engineer"),
		Kind:      "github.ci_failed",
		CreatedAt: t0,
	}
	if _, err := store.Enqueue(ctx, event); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(ctx, "worker-a", t0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []agent.State{agent.StateWaking, agent.StatePlanning} {
		if _, err := store.Transition(ctx, first.Lease, state, t0); err != nil {
			t.Fatal(err)
		}
	}
	args, err := json.Marshal(map[string]any{
		"owner": owner,
		"repo": repo,
		"head": head,
		"base": "main",
		"expected_head_sha": headSHA,
		"title": "A7 live recovery fixture 20261008-0652",
		"body": "A7 live recovery fixture.",
		"draft": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := cognition.Plan{
		ID: "plan-" + eventID,
		AgentID: agent.AgentID("release-engineer"),
		EventID: eventID,
		ProviderRef: "a7://precreated-real-github",
		Effects: []cognition.ProposedEffect{{
			ID: "open-pr",
			Tool: releaseengineer.ToolOpenPullRequest,
			Action: releaseengineer.ActionOpenPullRequest,
			Arguments: args,
		}},
	}
	binding, err := agentruntime.NewPlanBinding(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindPlan(ctx, first.Lease, binding, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, first.Lease, agent.StateWaitingForAdmission, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := agentruntime.BeginRemoteExecution(ctx, store, first.Lease, admissionClient, t0); err != nil {
		t.Fatal(err)
	}

	takeoverAt := t0.Add(2 * time.Second)
	setClock(takeoverAt)
	second, err := store.Claim(ctx, "worker-b", takeoverAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.LifecycleState != agent.StateUnknown {
		t.Fatalf("takeover state = %s, want UNKNOWN", second.Record.LifecycleState)
	}

	realClient := githubpr.Client{Token: token}
	handler := releaseengineer.Handler{
		Store: store,
		Admission: admissionClient,
		GitHub: realClient,
		Clock: clock,
	}
	if err := handler.Handle(ctx, second); err != nil {
		t.Fatalf("recover precreated real GitHub effect: %v", err)
	}
	recovered, err := store.Get(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.LifecycleState != agent.StateSleeping {
		t.Fatalf("lifecycle = %s, want SLEEPING", recovered.LifecycleState)
	}
	if recovered.Resolution == nil || recovered.Resolution.Outcome != agentruntime.EffectAppliedOnce {
		t.Fatalf("resolution = %+v, want APPLIED_ONCE", recovered.Resolution)
	}

	authorizeMu.Lock()
	gotAuthorizations := authorizeCount
	authorizeMu.Unlock()
	if gotAuthorizations != 1 {
		t.Fatalf("admission calls = %d, want 1", gotAuthorizations)
	}
	if _, err := store.Complete(ctx, second.Lease, takeoverAt); err != nil {
		t.Fatal(err)
	}
	t.Log("A7 provider recovery PASS: real GitHub APPLIED_ONCE closed UNKNOWN without reauthorization or replay")
}
