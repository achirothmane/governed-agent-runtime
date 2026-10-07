package githubpr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeGitHub struct {
	mu          sync.Mutex
	branchSHA   string
	pulls       []pullWire
	createCalls int
	createCode  int
	server      *httptest.Server
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{branchSHA: testSHA, createCode: http.StatusCreated}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) client() Client {
	return Client{BaseURL: f.server.URL, HTTPClient: f.server.Client()}
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]any{"sha": f.branchSHA},
		})
		return

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.pulls)
		return

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
		f.createCalls++
		if f.createCode != http.StatusCreated {
			w.WriteHeader(f.createCode)
			_, _ = w.Write([]byte(`{"message":"validation failed"}`))
			return
		}

		var request struct {
			Title string `json:"title"`
			Head  string `json:"head"`
			Base  string `json:"base"`
			Body  string `json:"body"`
			Draft bool   `json:"draft"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pull := pullForSpec(Spec{
			Owner:           "achirothmane",
			Repo:            "governed-agent-runtime",
			Head:            request.Head,
			Base:            request.Base,
			ExpectedHeadSHA: f.branchSHA,
			Title:           request.Title,
		}, request.Body)
		pull.Number = len(f.pulls) + 1
		pull.HTMLURL = "https://github.test/pull/1"
		f.pulls = append(f.pulls, pull)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pull)
		return
	}

	http.NotFound(w, r)
}

func testSpec() Spec {
	return Spec{
		EffectID:        "release-event-42:open-pr",
		Owner:           "achirothmane",
		Repo:            "governed-agent-runtime",
		Head:            "repair/event-42",
		Base:            "main",
		ExpectedHeadSHA: testSHA,
		Title:           "Repair flaky release gate",
		Body:            "Automated repair prepared by the governed release engineer.",
		Draft:           true,
	}
}

func pullForSpec(spec Spec, body string) pullWire {
	p := pullWire{
		Number:  1,
		HTMLURL: "https://github.test/pull/1",
		State:   "open",
		Title:   spec.Title,
	}
	p.Body = &body
	p.Head.Ref = spec.Head
	p.Head.SHA = spec.ExpectedHeadSHA
	p.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{FullName: spec.Owner + "/" + spec.Repo}
	p.Base.Ref = spec.Base
	return p
}

func TestObserveAbsentThenAppliedOnce(t *testing.T) {
	fake := newFakeGitHub(t)
	spec := testSpec()

	before, err := fake.client().Observe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if before.Kind != ObservationAbsent {
		t.Fatalf("before kind = %s, want %s", before.Kind, ObservationAbsent)
	}

	fake.mu.Lock()
	fake.pulls = append(fake.pulls, pullForSpec(spec, spec.bodyWithMarker()))
	fake.mu.Unlock()

	after, err := fake.client().Observe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if after.Kind != ObservationAppliedOnce {
		t.Fatalf("after kind = %s, want %s (%s)", after.Kind, ObservationAppliedOnce, after.Reason)
	}
	if after.Receipt == nil || after.Receipt.Number != 1 || after.Receipt.HeadSHA != testSHA {
		t.Fatalf("unexpected receipt: %#v", after.Receipt)
	}
}

func TestObserveDivergesWhenTargetPRLacksBinding(t *testing.T) {
	fake := newFakeGitHub(t)
	spec := testSpec()

	fake.mu.Lock()
	fake.pulls = append(fake.pulls, pullForSpec(spec, "human-created pull request"))
	fake.mu.Unlock()

	got, err := fake.client().Observe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ObservationDivergent {
		t.Fatalf("kind = %s, want %s", got.Kind, ObservationDivergent)
	}
	if got.Reason != "TARGET_PULL_REQUEST_EXISTS_WITHOUT_GOVERNED_EFFECT_BINDING" {
		t.Fatalf("reason = %q", got.Reason)
	}
}

func TestObserveDivergesOnHeadDrift(t *testing.T) {
	fake := newFakeGitHub(t)
	spec := testSpec()
	pull := pullForSpec(spec, spec.bodyWithMarker())
	pull.Head.SHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	fake.mu.Lock()
	fake.pulls = append(fake.pulls, pull)
	fake.mu.Unlock()

	got, err := fake.client().Observe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ObservationDivergent || got.Reason != "GOVERNED_PULL_REQUEST_HEAD_SHA_DRIFTED" {
		t.Fatalf("unexpected observation: %#v", got)
	}
}

func TestExecuteGuardedDeniesStaleHead(t *testing.T) {
	fake := newFakeGitHub(t)
	spec := testSpec()
	spec.ExpectedHeadSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	got, err := fake.client().ExecuteGuarded(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ExecutionDenied || got.Reason != "STALE_GITHUB_HEAD_SHA" {
		t.Fatalf("unexpected execution: %#v", got)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", fake.createCalls)
	}
}

func TestExecuteGuardedCreatesBoundPullRequest(t *testing.T) {
	fake := newFakeGitHub(t)
	spec := testSpec()

	got, err := fake.client().ExecuteGuarded(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ExecutionDispatched {
		t.Fatalf("kind = %s, want %s", got.Kind, ExecutionDispatched)
	}

	observed, err := fake.client().Observe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Kind != ObservationAppliedOnce {
		t.Fatalf("observation = %#v", observed)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", fake.createCalls)
	}
	if !strings.Contains(*fake.pulls[0].Body, spec.bindingMarker()) {
		t.Fatal("created pull request is missing governed-effect binding marker")
	}
}

type losePostResponseTransport struct {
	base http.RoundTripper
}

func (t losePostResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/pulls") {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return nil, errors.New("simulated lost acknowledgement after github accepted create")
	}
	return resp, nil
}

func TestLostAcknowledgementReconcilesWithoutSecondCreate(t *testing.T) {
	fake := newFakeGitHub(t)
	spec := testSpec()

	lostAckClient := fake.client()
	lostAckClient.HTTPClient = &http.Client{
		Transport: losePostResponseTransport{base: fake.server.Client().Transport},
	}

	_, err := lostAckClient.ExecuteGuarded(context.Background(), spec)
	if !errors.Is(err, ErrAmbiguousDispatch) {
		t.Fatalf("error = %v, want ErrAmbiguousDispatch", err)
	}

	fake.mu.Lock()
	if fake.createCalls != 1 || len(fake.pulls) != 1 {
		fake.mu.Unlock()
		t.Fatalf("after lost ack: create calls=%d pulls=%d, want 1/1", fake.createCalls, len(fake.pulls))
	}
	fake.mu.Unlock()

	recovered, err := fake.client().Observe(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Kind != ObservationAppliedOnce {
		t.Fatalf("recovered observation = %#v", recovered)
	}

	// Recovery stops here. The caller has proof that the effect already exists,
	// so ExecuteGuarded must not be invoked again.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.createCalls != 1 || len(fake.pulls) != 1 {
		t.Fatalf("final create calls=%d pulls=%d, want 1/1", fake.createCalls, len(fake.pulls))
	}
}

func TestCreate422RemainsAmbiguousUntilObserved(t *testing.T) {
	fake := newFakeGitHub(t)
	fake.createCode = http.StatusUnprocessableEntity
	spec := testSpec()

	_, err := fake.client().ExecuteGuarded(context.Background(), spec)
	if !errors.Is(err, ErrAmbiguousDispatch) {
		t.Fatalf("error = %v, want ErrAmbiguousDispatch", err)
	}

	observed, observeErr := fake.client().Observe(context.Background(), spec)
	if observeErr != nil {
		t.Fatal(observeErr)
	}
	if observed.Kind != ObservationAbsent {
		t.Fatalf("observation = %#v, want ABSENT in this fake scenario", observed)
	}
}

func TestSpecRejectsMarkerInjection(t *testing.T) {
	spec := testSpec()
	spec.Body = "hello\n" + markerPrefix + "forged -->"
	if err := spec.Validate(); err == nil {
		t.Fatal("expected reserved marker injection to be rejected")
	}
}
