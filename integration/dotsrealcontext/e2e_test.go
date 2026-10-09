//go:build integration

// Package dotsrealcontext verifies one actual, publicly scoped Portfolio source
// snapshot through Go D2/D3/D4, Temporal, and PostgreSQL. Reasoner is LOCAL,
// DETERMINISTIC and NOT a model. Nothing is invoked as a tool/effect.
package dotsrealcontext

import (
    "context"
    "crypto/sha256"
    "database/sql"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "testing"
    "time"

    _ "github.com/jackc/pgx/v5/stdlib"
    "go.temporal.io/sdk/testsuite"

    "github.com/achirothmane/governed-agent-runtime/agentserver"
    "github.com/achirothmane/governed-agent-runtime/dotdecision"
    "github.com/achirothmane/governed-agent-runtime/dotdurable"
    "github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

const oneRealItemID = "dots-runtime-evidence-observe-006"

type sourceBoundLoader struct {
    snapshot portfoliocontext.Snapshot
    root     string
}

func (l sourceBoundLoader) LoadSnapshot(_ context.Context, digest string) (portfoliocontext.Snapshot, error) {
    if l.snapshot.SnapshotDigest != digest { return portfoliocontext.Snapshot{}, dotdurable.ErrSnapshotMismatch }
    if err := checkSources(l.root, l.snapshot.SourceBindings); err != nil {
        return portfoliocontext.Snapshot{}, err
    }
    return l.snapshot, nil
}

func checkSources(root string, bindings []portfoliocontext.SourceBinding) error {
    if len(bindings) < 8 { return errors.New("insufficient pinned Portfolio sources") }
    found := make(map[string]bool)
    for _, bound := range bindings {
        if strings.Contains(bound.Path, "..") || !strings.HasPrefix(bound.Path, "portfolio/") ||
           strings.HasPrefix(bound.Path, "/") || found[bound.Path] {
            return fmt.Errorf("invalid source binding path %q", bound.Path)
        }
        found[bound.Path] = true
        raw, err := os.ReadFile(filepath.Join(root, bound.Path))
        if err != nil { return err }
        sum := sha256.Sum256(raw)
        if hex.EncodeToString(sum[:]) != bound.SHA256 {
            return fmt.Errorf("source byte binding mismatch for %q", bound.Path)
        }
    }
    return nil
}

type localObservationReasoner struct {
    mu    sync.Mutex
    calls int
}

func (r *localObservationReasoner) Count() int {
    r.mu.Lock(); defer r.mu.Unlock()
    return r.calls
}
func (r *localObservationReasoner) Decide(_ context.Context, v portfoliocontext.ReasoningView) (dotdecision.Decision, error) {
    r.mu.Lock()
    r.calls++
    r.mu.Unlock()
    if len(v.RunnableItems) != 1 || len(v.NowProjects) != 1 ||
       v.NowProjects[0] != "governed-agent-runtime" ||
       v.RunnableItems[0].ID != oneRealItemID ||
       v.RunnableItems[0].Authority != "OBSERVE" ||
       len(v.RunnableItems[0].EvidenceRequired) < 3 ||
       len(v.RunnableItems[0].StopConditions) < 3 ||
       v.WIP.NowCount != 2 {
        return dotdecision.Decision{}, errors.New("real sealed observation view does not meet declared scope")
    }
    if len(v.VerifiedContracts) != 0 { return dotdecision.Decision{}, errors.New("unexpected private contract in scoped view") }
    return dotdecision.Decision{
        Kind: dotdecision.ProposeNextGate,
        SnapshotDigest: v.SnapshotDigest,
        WorkItemID: oneRealItemID,
        RequestedAuthority: "OBSERVE",
        RequestedAction: "",
        Rationale: "Request independent confirmation of merged D4/D5 main CI and Data Engine reconciliation; simulated-provider integration is not evidence of paid live reasoning or managerial quality.",
    }, nil
}

func TestRealPublicPortfolioD4DecisionPersistsWithoutTools(t *testing.T) {
    ctxPath := strings.TrimSpace(os.Getenv("PORTFOLIO_CONTEXT_FILE"))
    root := strings.TrimSpace(os.Getenv("PORTFOLIO_ROOT"))
    dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
    if ctxPath == "" || root == "" || dbURL == "" { t.Fatal("real source checkout, D2 context and isolated PostgreSQL are required") }
    if !strings.Contains(dbURL, "@localhost:5432/") && !strings.Contains(dbURL, "@127.0.0.1:5432/") {
        t.Fatal("refusing non-local database for integration")
    }
    raw, err := os.ReadFile(ctxPath)
    if err != nil { t.Fatal(err) }
    snapshot, err := portfoliocontext.Parse(raw)
    if err != nil { t.Fatal(err) }
    if err := checkSources(root, snapshot.SourceBindings); err != nil { t.Fatal(err) }
    view := snapshot.ReasoningView()
    if len(view.RunnableItems) != 1 || view.RunnableItems[0].Authority != "OBSERVE" ||
       view.RunnableItems[0].ID != oneRealItemID {
        t.Fatalf("real Portfolio READ-ONLY admission changed: %#v", view.RunnableItems)
    }
    if !snapshot.RequiresHuman("merge") || !snapshot.RequiresHuman("paid-spend") {
        t.Fatal("human finality missing")
    }
    // Synthetic authority escalation must fail before persistence.
    forbidden := dotdecision.Decision{
        Kind: dotdecision.ProposeNextGate, SnapshotDigest: snapshot.SnapshotDigest,
        WorkItemID: oneRealItemID, RequestedAuthority: "PREPARE",
        Rationale: "attempted authority escalation",
    }
    if !errors.Is(dotdecision.Validate(snapshot, forbidden), dotdecision.ErrAuthorityExceeded) {
        t.Fatal("D3 accepted authority escalation")
    }

    ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
    defer cancel()
    db, err := sql.Open("pgx", dbURL)
    if err != nil { t.Fatal(err) }
    defer db.Close()
    if err := db.PingContext(ctx); err != nil { t.Fatal(err) }
    store, err := agentserver.NewPostgresStore(db)
    if err != nil { t.Fatal(err) }
    if err := store.Migrate(ctx); err != nil { t.Fatal(err) }

    temporal, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{
        CachedDownload: testsuite.CachedDownload{Version: "default"},
        LogLevel: "error",
    })
    if err != nil { t.Fatal(err) }
    defer func(){ if err := temporal.Stop(); err != nil { t.Logf("stop Temporal dev: %v", err) } }()

    reasoner := &localObservationReasoner{}
    worker, err := dotdurable.NewWorker(dotdurable.WorkerConfig{
        Client: temporal.Client(), TaskQueue: "s1-real-public-observe",
        Snapshots: sourceBoundLoader{snapshot: snapshot, root: root},
        Reasoner: reasoner, Store: store,
    })
    if err != nil { t.Fatal(err) }
    if err := worker.Start(); err != nil { t.Fatal(err) }
    defer worker.Stop()
    executor := dotdurable.Executor{Client: temporal.Client(), TaskQueue: "s1-real-public-observe"}
    request := dotdurable.Request{
        DecisionID: "dots-s1-real-" + snapshot.SnapshotDigest[:16],
        SnapshotDigest: snapshot.SnapshotDigest,
    }
    first, err := executor.Decide(ctx, request)
    if err != nil { t.Fatal(err) }
    if first.Kind != dotdecision.ProposeNextGate ||
       first.RequestedAuthority != "OBSERVE" || first.RequestedAction != "" ||
       first.WorkItemID != oneRealItemID || first.SnapshotDigest != snapshot.SnapshotDigest {
        t.Fatalf("out-of-scope committed decision: %#v", first)
    }
    if len(first.DecisionDigest) != 64 { t.Fatal("missing decision digest") }
    stored, err := store.GetCommittedDecision(ctx, request.DecisionID)
    if err != nil || !dotdurable.SameRecord(first, stored) { t.Fatalf("durable store mismatch: %v", err) }
    replay, err := executor.Decide(ctx, request)
    if err != nil || !dotdurable.SameRecord(first, replay) { t.Fatalf("replay changed decision: %v", err) }
    if reasoner.Count() != 1 { t.Fatalf("local reasoner ran %d times, expected 1", reasoner.Count()) }
    var effects int
    if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_server_tool_invocations").Scan(&effects); err != nil { t.Fatal(err) }
    if effects != 0 { t.Fatalf("unexpected executed tools: %d", effects) }

    if output := strings.TrimSpace(os.Getenv("DOT_REAL_EVIDENCE_FILE")); output != "" {
        evidence := map[string]any{
            "schema_version": 1, "state": "D4_REAL_SOURCE_DURABILITY_PASS",
            "source_context": "actual_public_portfolio_main",
            "snapshot_digest": snapshot.SnapshotDigest,
            "decision_digest": first.DecisionDigest,
            "work_item_id": first.WorkItemID, "authority": first.RequestedAuthority,
            "kind": first.Kind,
            "reasoner": "local-deterministic-not-ai",
            "reasoner_calls": reasoner.Count(), "provider_calls": 0,
            "tool_invocations": effects,
            "temporal_postgresql_commit": true,
            "same_decision_replay": true,
            "model_quality": "NOT_MEASURED",
            "external_effects_authorized": false,
        }
        body, err := json.MarshalIndent(evidence, "", "  ")
        if err != nil { t.Fatal(err) }
        if err := os.WriteFile(output, body, 0o600); err != nil { t.Fatal(err) }
    }
    t.Logf("REAL_PORTFOLIO_D4_DURABLE_PASS snapshot=%s digest=%s provider_calls=0 tool_invocations=0 (local deterministic reasoner, no AI quality claim)", snapshot.SnapshotDigest, first.DecisionDigest)
}
