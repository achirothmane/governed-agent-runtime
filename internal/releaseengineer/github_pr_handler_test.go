package releaseengineer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
	"github.com/achirothmane/governed-agent-runtime/internal/cognition"
	"github.com/achirothmane/governed-agent-runtime/internal/effects/githubpr"
	"github.com/achirothmane/governed-agent-runtime/internal/governance"
	agentruntime "github.com/achirothmane/governed-agent-runtime/internal/runtime"
)

const releaseHeadSHA = "0123456789abcdef0123456789abcdef01234567"

type githubFake struct {
	mu          sync.Mutex
	branchSHA   string
	pulls       []map[string]any
	createCalls int
	server      *httptest.Server
}

func newGitHubFake(t *testing.T) *githubFake {
	t.Helper()
	f := &githubFake{branchSHA: releaseHeadSHA}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *githubFake) client() githubpr.Client {
	return githubpr.Client{BaseURL: f.server.URL, HTTPClient: f.server.Client()}
}

func (f *githubFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/"):
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]any{"sha": f.branchSHA},
		})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
		_ = json.NewEncoder(w).Encode(f.pulls)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
		f.createCalls++
		var in struct {
			Title string `json:"title"`
			Head  string `json:"head"`
			Base  string `json:"base"`
			Body  string `json:"body"`
			Draft bool   `json:"draft"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pull := map[string]any{
			"number":   len(f.pulls) + 1,
			"html_url": fmt.Sprintf("https://github.test/pull/%d", len(f.pulls)+1),
			"state":    "open",
			"title":    in.Title,
			"body":     in.Body,
			"head": map[string]any{
				"ref": in.Head,
				"sha": f.branchSHA,
				"repo": map[string]any{
					"full_name": "achirothmane/governed-agent-runtime",
				},
			},
			"base": map[string]any{"ref": in.Base},
		}
		f.pulls = append(f.pulls, pull)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pull)
	default:
		http.NotFound(w, r)
	}
}

func (f *githubFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCalls
}

type fixture struct {
	store          agentruntime.Store
	admission      *governance.Client
	github         *githubFake
	current        time.Time
	authorizeCount int
	requests       []governance.AdmissionRequest
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pub, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		current: time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC),
		github:  newGitHubFake(t),
	}
	witness := governance.Witness{
		EvidenceDigest: "release-evidence-v1",
		AuthorityRef:   "release-authority-v1",
		PolicyHash:     "release-policy-v1",
		DoctrineEpoch:  1,
	}
	service, err := governance.NewService(governance.ServiceConfig{
		PrivateKey:  privateKey,
		BearerToken: "test-token",
		Clock:       func() time.Time { return f.current },
		Authorize: func(_ context.Context, request governance.AdmissionRequest) (governance.Grant, error) {
			f.authorizeCount++
			f.requests = append(f.requests, request)
			return governance.Grant{
				Target:     "github:achirothmane/governed-agent-runtime",
				Profile:    AdmissionProfile,
				Witness:    witness,
				ValidUntil: f.current.Add(time.Hour),
			}, nil
		},
		Revalidate: func(context.Context, governance.Admission) (governance.Witness, error) {
			return witness, nil
		},
		MaxAdmissions: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	admissionServer := httptest.NewServer(service)
	t.Cleanup(admissionServer.Close)
	f.admission, err = governance.NewClient(admissionServer.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}

	store, err := agentruntime.NewFileStoreWithGovernance(
		filepath.Join(t.TempDir(), "release-runtime.json"),
		f.admission.Verifier(pub, func() time.Time { return f.current }),
	)
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
	return f
}

func (f *fixture) plan(eventID string) cognition.Plan {
	args, _ := json.Marshal(pullArguments{
		Owner:           "achirothmane",
		Repo:            "governed-agent-runtime",
		Head:            "repair/" + eventID,
		Base:            "main",
		ExpectedHeadSHA: releaseHeadSHA,
		Title:           "Repair " + eventID,
		Body:            "Prepared by governed release engineer.",
		Draft:           true,
	})
	return cognition.Plan{
		ID:          "plan-" + eventID,
		AgentID:     agent.AgentID("release-engineer"),
		EventID:     eventID,
		ProviderRef: "test://release-planner/v1",
		Effects: []cognition.ProposedEffect{{
			ID:        "open-pr",
			Tool:      ToolOpenPullRequest,
			Action:    ActionOpenPullRequest,
			Arguments: args,
		}},
	}
}

func (f *fixture) prepare(t *testing.T, eventID, worker string, ttl time.Duration) agentruntime.ClaimedWork {
	t.Helper()
	ctx := context.Background()
	event := agentruntime.Event{
		ID:        eventID,
		AgentID:   agent.AgentID("release-engineer"),
		Kind:      "github.ci_failed",
		CreatedAt: f.current,
	}
	if _, err := f.store.Enqueue(ctx, event); err != nil {
		t.Fatal(err)
	}
	claim, err := f.store.Claim(ctx, worker, f.current, ttl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(ctx, claim.Lease, agent.StateWaking, f.current); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(ctx, claim.Lease, agent.StatePlanning, f.current); err != nil {
		t.Fatal(err)
	}
	binding, err := agentruntime.NewPlanBinding(f.plan(eventID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.BindPlan(ctx, claim.Lease, binding, f.current); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(ctx, claim.Lease, agent.StateWaitingForAdmission, f.current); err != nil {
		t.Fatal(err)
	}
	return claim
}

func (f *fixture) handler() Handler {
	return Handler{
		Store:     f.store,
		Admission: f.admission,
		GitHub:    f.github.client(),
		Clock:     func() time.Time { return f.current },
	}
}

func TestReleaseEngineerNormalEffectCompletesThroughAdmissionAndObservation(t *testing.T) {
	f := newFixture(t)
	claim := f.prepare(t, "evt-normal", "worker-a", time.Minute)

	if err := f.handler().Handle(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	record, err := f.store.Get(context.Background(), claim.Lease.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if record.LifecycleState != agent.StateSleeping {
		t.Fatalf("lifecycle = %s, want SLEEPING", record.LifecycleState)
	}
	if f.authorizeCount != 1 || f.github.calls() != 1 {
		t.Fatalf("authorize/create calls = %d/%d, want 1/1", f.authorizeCount, f.github.calls())
	}
	if _, err := f.store.Complete(context.Background(), claim.Lease, f.current); err != nil {
		t.Fatalf("complete verified work: %v", err)
	}
}

func TestTakeoverAfterAppliedEffectReconcilesWithoutNewAdmissionOrCreate(t *testing.T) {
	f := newFixture(t)
	first := f.prepare(t, "evt-applied-takeover", "worker-a", 10*time.Second)

	if _, err := agentruntime.BeginRemoteExecution(context.Background(), f.store, first.Lease, f.admission, f.current); err != nil {
		t.Fatal(err)
	}
	spec, _, err := specFromRecord(mustGet(t, f.store, first.Lease.EventID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.github.client().ExecuteGuarded(context.Background(), spec); err != nil {
		t.Fatal(err)
	}

	f.current = f.current.Add(11 * time.Second)
	second, err := f.store.Claim(context.Background(), "worker-b", f.current, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.LifecycleState != agent.StateUnknown {
		t.Fatalf("takeover lifecycle = %s, want UNKNOWN", second.Record.LifecycleState)
	}

	if err := f.handler().Handle(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	record := mustGet(t, f.store, second.Lease.EventID)
	if record.LifecycleState != agent.StateSleeping {
		t.Fatalf("lifecycle = %s, want SLEEPING", record.LifecycleState)
	}
	if record.Resolution == nil || record.Resolution.Outcome != agentruntime.EffectAppliedOnce {
		t.Fatalf("resolution = %+v", record.Resolution)
	}
	if f.authorizeCount != 1 {
		t.Fatalf("authorization calls = %d, want 1", f.authorizeCount)
	}
	if f.github.calls() != 1 {
		t.Fatalf("GitHub create calls = %d, want 1", f.github.calls())
	}
}

func TestTakeoverWithProvedAbsenceObtainsFreshAdmissionThenExecutesOnce(t *testing.T) {
	f := newFixture(t)
	first := f.prepare(t, "evt-absent-takeover", "worker-a", 10*time.Second)

	if _, err := agentruntime.BeginRemoteExecution(context.Background(), f.store, first.Lease, f.admission, f.current); err != nil {
		t.Fatal(err)
	}
	// Worker A disappears before calling GitHub.
	f.current = f.current.Add(11 * time.Second)
	second, err := f.store.Claim(context.Background(), "worker-b", f.current, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.LifecycleState != agent.StateUnknown {
		t.Fatalf("takeover lifecycle = %s, want UNKNOWN", second.Record.LifecycleState)
	}

	if err := f.handler().Handle(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	record := mustGet(t, f.store, second.Lease.EventID)
	if record.LifecycleState != agent.StateSleeping {
		t.Fatalf("lifecycle = %s, want SLEEPING", record.LifecycleState)
	}
	if record.Resolution == nil || record.Resolution.Outcome != agentruntime.EffectAbsent {
		t.Fatalf("resolution = %+v", record.Resolution)
	}
	if f.authorizeCount != 2 {
		t.Fatalf("authorization calls = %d, want 2", f.authorizeCount)
	}
	if len(f.requests) != 2 ||
		f.requests[0].Subject.LeaseEpoch == f.requests[1].Subject.LeaseEpoch ||
		f.requests[1].Subject.LeaseEpoch != second.Lease.Epoch ||
		f.requests[1].Subject.WorkerID != "worker-b" {
		t.Fatalf("admission subjects were not rebound to takeover lease: %#v", f.requests)
	}
	if f.github.calls() != 1 {
		t.Fatalf("GitHub create calls = %d, want 1", f.github.calls())
	}
}

func mustGet(t *testing.T, store agentruntime.Store, eventID string) agentruntime.WorkRecord {
	t.Helper()
	record, err := store.Get(context.Background(), eventID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
