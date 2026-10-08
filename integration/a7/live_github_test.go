package a7

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

const helperCrashExitCode = 86

type liveGitHub struct {
	token string
	owner string
	repo  string
	http  *http.Client
}

func newLiveGitHub(token, owner, repo string) liveGitHub {
	return liveGitHub{
		token: token,
		owner: owner,
		repo:  repo,
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

func (g liveGitHub) request(ctx context.Context, method, endpoint string, input, output any, want ...int) error {
	var body io.Reader
	if input != nil {
		document, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(document)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.github.com"+endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("Authorization", "Bearer "+g.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	allowed := false
	for _, status := range want {
		if resp.StatusCode == status {
			allowed = true
			break
		}
	}
	if !allowed {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		return fmt.Errorf("%s %s: http %d: %s", method, endpoint, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if output != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			return err
		}
	}
	return nil
}

func (g liveGitHub) refSHA(ctx context.Context, branch string) (string, error) {
	var response struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := g.request(
		ctx,
		http.MethodGet,
		"/repos/"+url.PathEscape(g.owner)+"/"+url.PathEscape(g.repo)+"/git/ref/heads/"+escapeBranch(branch),
		nil,
		&response,
		http.StatusOK,
	)
	return response.Object.SHA, err
}

func (g liveGitHub) createBranch(ctx context.Context, branch, fromSHA string) error {
	return g.request(
		ctx,
		http.MethodPost,
		"/repos/"+url.PathEscape(g.owner)+"/"+url.PathEscape(g.repo)+"/git/refs",
		map[string]any{"ref": "refs/heads/" + branch, "sha": fromSHA},
		nil,
		http.StatusCreated,
	)
}

func (g liveGitHub) addBranchCommit(ctx context.Context, branch, runID string) error {
	content := base64.StdEncoding.EncodeToString([]byte("A7 real GitHub recovery probe " + runID + "\n"))
	return g.request(
		ctx,
		http.MethodPut,
		"/repos/"+url.PathEscape(g.owner)+"/"+url.PathEscape(g.repo)+"/contents/"+url.PathEscape(".a7-"+runID+".txt"),
		map[string]any{
			"message": "test(a7): real GitHub recovery probe " + runID,
			"content": content,
			"branch":  branch,
		},
		nil,
		http.StatusCreated,
	)
}

func (g liveGitHub) closePull(ctx context.Context, number int) error {
	return g.request(
		ctx,
		http.MethodPatch,
		fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(g.owner), url.PathEscape(g.repo), number),
		map[string]any{"state": "closed"},
		nil,
		http.StatusOK,
	)
}

func (g liveGitHub) deleteBranch(ctx context.Context, branch string) error {
	return g.request(
		ctx,
		http.MethodDelete,
		"/repos/"+url.PathEscape(g.owner)+"/"+url.PathEscape(g.repo)+"/git/refs/heads/"+escapeBranch(branch),
		nil,
		nil,
		http.StatusNoContent,
		http.StatusNotFound,
	)
}

func escapeBranch(branch string) string {
	parts := strings.Split(branch, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

type lostAckTransport struct {
	base http.RoundTripper
}

func (t lostAckTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/pulls") && resp.StatusCode == http.StatusCreated {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, errors.New("A7 injected lost acknowledgement after GitHub accepted create")
	}
	return resp, nil
}

type crashAfterLostAckBoundary struct {
	client githubpr.Client
}

func (b crashAfterLostAckBoundary) Observe(ctx context.Context, spec githubpr.Spec) (githubpr.Observation, error) {
	return b.client.Observe(ctx, spec)
}

func (b crashAfterLostAckBoundary) ExecuteGuarded(ctx context.Context, spec githubpr.Spec) (githubpr.Execution, error) {
	result, err := b.client.ExecuteGuarded(ctx, spec)
	if errors.Is(err, githubpr.ErrAmbiguousDispatch) {
		os.Exit(helperCrashExitCode)
	}
	return githubpr.Execution{}, fmt.Errorf(
		"A7 helper expected ambiguous lost-ack dispatch, got kind=%s reason=%s err=%v",
		result.Kind,
		result.Reason,
		err,
	)
}

func TestA7CrashHelperProcess(t *testing.T) {
	if os.Getenv("A7_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}

	now, err := time.Parse(time.RFC3339Nano, os.Getenv("A7_NOW"))
	if err != nil {
		t.Fatal(err)
	}
	publicKeyBytes, err := base64.StdEncoding.DecodeString(os.Getenv("A7_ADMISSION_PUBLIC_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := governance.NewClient(os.Getenv("A7_ADMISSION_URL"), os.Getenv("A7_ADMISSION_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentruntime.NewFileStoreWithGovernance(
		os.Getenv("A7_STORE_PATH"),
		client.Verifier(ed25519.PublicKey(publicKeyBytes), func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), os.Getenv("A7_EVENT_ID"))
	if err != nil {
		t.Fatal(err)
	}
	lease := agentruntime.LeaseToken{
		EventID:   record.Event.ID,
		WorkerID:  record.LeaseOwner,
		Epoch:     record.LeaseEpoch,
		ExpiresAt: record.LeaseExpiresAt,
	}

	baseTransport := http.DefaultTransport
	httpClient := &http.Client{
		Timeout:   20 * time.Second,
		Transport: lostAckTransport{base: baseTransport},
	}
	boundary := crashAfterLostAckBoundary{
		client: githubpr.Client{
			Token:      os.Getenv("A7_GITHUB_TOKEN"),
			HTTPClient: httpClient,
		},
	}
	handler := releaseengineer.Handler{
		Store:     store,
		Admission: client,
		GitHub:    boundary,
		Clock:     func() time.Time { return now },
	}
	if err := handler.Handle(context.Background(), agentruntime.ClaimedWork{Record: record, Lease: lease}); err != nil {
		t.Fatalf("helper handler returned before injected crash: %v", err)
	}
	t.Fatal("helper did not crash after the injected lost acknowledgement")
}

func TestRealGitHubReleaseEngineerLostAckTakeover(t *testing.T) {
	if os.Getenv("A7_REAL_GITHUB") != "1" {
		t.Skip("set A7_REAL_GITHUB=1 in the isolated live workflow")
	}
	token := strings.TrimSpace(os.Getenv("A7_GITHUB_TOKEN"))
	owner := strings.TrimSpace(os.Getenv("A7_OWNER"))
	repo := strings.TrimSpace(os.Getenv("A7_REPO"))
	runID := strings.TrimSpace(os.Getenv("A7_RUN_ID"))
	if token == "" || owner == "" || repo == "" || runID == "" {
		t.Fatal("A7 live GitHub environment is incomplete")
	}

	ctx := context.Background()
	live := newLiveGitHub(token, owner, repo)
	base := "main"
	baseSHA, err := live.refSHA(ctx, base)
	if err != nil {
		t.Fatalf("read real base ref: %v", err)
	}
	head := "a7-real-e2e/" + runID
	if err := live.createBranch(ctx, head, baseSHA); err != nil {
		t.Fatalf("create real test branch: %v", err)
	}
	var cleanupPR int
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if cleanupPR != 0 {
			if err := live.closePull(cleanupCtx, cleanupPR); err != nil {
				t.Errorf("close A7 pull request: %v", err)
			}
		}
		if err := live.deleteBranch(cleanupCtx, head); err != nil {
			t.Errorf("delete A7 branch: %v", err)
		}
	})
	if err := live.addBranchCommit(ctx, head, runID); err != nil {
		t.Fatalf("add real test commit: %v", err)
	}
	headSHA, err := live.refSHA(ctx, head)
	if err != nil {
		t.Fatalf("read real test head: %v", err)
	}
	if headSHA == baseSHA {
		t.Fatal("A7 branch did not advance beyond base")
	}

	t0 := time.Date(2026, 10, 8, 6, 45, 0, 0, time.UTC)
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
		EvidenceDigest: "a7-live-github",
		AuthorityRef:   "a7-live-release-authority",
		PolicyHash:     "a7-live-policy",
		DoctrineEpoch:  1,
	}
	var authorizeMu sync.Mutex
	authorizeCount := 0
	service, err := governance.NewService(governance.ServiceConfig{
		PrivateKey:  privateKey,
		BearerToken: "a7-admission-token",
		Clock:       clock,
		Authorize: func(_ context.Context, request governance.AdmissionRequest) (governance.Grant, error) {
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
	t.Cleanup(admissionServer.Close)
	admissionClient, err := governance.NewClient(admissionServer.URL, "a7-admission-token")
	if err != nil {
		t.Fatal(err)
	}

	storePath := filepath.Join(t.TempDir(), "a7-runtime.json")
	store, err := agentruntime.NewFileStoreWithGovernance(
		storePath,
		admissionClient.Verifier(pub, clock),
	)
	if err != nil {
		t.Fatal(err)
	}
	eventID := "a7-" + runID
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
	if _, err := store.Transition(ctx, first.Lease, agent.StateWaking, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, first.Lease, agent.StatePlanning, t0); err != nil {
		t.Fatal(err)
	}

	args, err := json.Marshal(map[string]any{
		"owner":             owner,
		"repo":              repo,
		"head":              head,
		"base":              base,
		"expected_head_sha": headSHA,
		"title":             "A7 real Release Engineer recovery " + runID,
		"body":              "Ephemeral A7 proof. This pull request must be closed and its branch deleted by test cleanup.",
		"draft":             true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := cognition.Plan{
		ID:          "plan-" + eventID,
		AgentID:     agent.AgentID("release-engineer"),
		EventID:     eventID,
		ProviderRef: "a7://real-github",
		Effects: []cognition.ProposedEffect{{
			ID:        "open-pr",
			Tool:      releaseengineer.ToolOpenPullRequest,
			Action:    releaseengineer.ActionOpenPullRequest,
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

	helper := exec.Command(os.Args[0], "-test.run=^TestA7CrashHelperProcess$", "-test.v")
	helper.Env = append(os.Environ(),
		"A7_HELPER=1",
		"A7_STORE_PATH="+storePath,
		"A7_EVENT_ID="+eventID,
		"A7_GITHUB_TOKEN="+token,
		"A7_ADMISSION_URL="+admissionServer.URL,
		"A7_ADMISSION_TOKEN=a7-admission-token",
		"A7_ADMISSION_PUBLIC_KEY="+base64.StdEncoding.EncodeToString(pub),
		"A7_NOW="+t0.Format(time.RFC3339Nano),
	)
	var helperOutput bytes.Buffer
	helper.Stdout = &helperOutput
	helper.Stderr = &helperOutput
	err = helper.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != helperCrashExitCode {
		t.Fatalf("helper did not crash at the lost-ack boundary: err=%v output=%s", err, helperOutput.String())
	}

	// The child died while the durable state still said EXECUTING. A later owner
	// must turn that into UNKNOWN before it can consult GitHub.
	takeoverAt := t0.Add(2 * time.Second)
	setClock(takeoverAt)
	store, err = agentruntime.NewFileStoreWithGovernance(
		storePath,
		admissionClient.Verifier(pub, clock),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Claim(ctx, "worker-b", takeoverAt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.LifecycleState != agent.StateUnknown {
		t.Fatalf("takeover state = %s, want UNKNOWN", second.Record.LifecycleState)
	}

	realClient := githubpr.Client{Token: token}
	handler := releaseengineer.Handler{
		Store:     store,
		Admission: admissionClient,
		GitHub:    realClient,
		Clock:     clock,
	}
	if err := handler.Handle(ctx, second); err != nil {
		t.Fatalf("real GitHub takeover recovery: %v", err)
	}
	recovered, err := store.Get(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.LifecycleState != agent.StateSleeping {
		t.Fatalf("recovered lifecycle = %s, want SLEEPING", recovered.LifecycleState)
	}
	if recovered.Resolution == nil || recovered.Resolution.Outcome != agentruntime.EffectAppliedOnce {
		t.Fatalf("recovery proof = %+v, want APPLIED_ONCE", recovered.Resolution)
	}

	observation, err := realClient.Observe(ctx, githubpr.Spec{
		EffectID:        eventID + ":open-pr",
		Owner:           owner,
		Repo:            repo,
		Head:            head,
		Base:            base,
		ExpectedHeadSHA: headSHA,
		Title:           "A7 real Release Engineer recovery " + runID,
		Body:            "Ephemeral A7 proof. This pull request must be closed and its branch deleted by test cleanup.",
		Draft:           true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Kind != githubpr.ObservationAppliedOnce || observation.Receipt == nil {
		t.Fatalf("final provider observation = %+v", observation)
	}
	cleanupPR = observation.Receipt.Number

	authorizeMu.Lock()
	gotAuthorizations := authorizeCount
	authorizeMu.Unlock()
	if gotAuthorizations != 1 {
		t.Fatalf("admission calls = %d, want exactly 1; APPLIED_ONCE takeover must not reauthorize", gotAuthorizations)
	}
	if _, err := store.Complete(ctx, second.Lease, takeoverAt); err != nil {
		t.Fatalf("complete recovered A7 work: %v", err)
	}
	t.Logf("A7 PASS: real GitHub PR #%d recovered after injected lost ACK with one admission and no replay", cleanupPR)
}
