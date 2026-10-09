//go:build integration && liveprovider

package dotsprovider

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/dotdurable"
	openaireasoner "github.com/achirothmane/governed-agent-runtime/dotproviders/openai"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

func TestOptInLiveOpenAIProviderSmoke(t *testing.T) {
	if strings.TrimSpace(os.Getenv("ALLOW_PAID_MODEL_TEST")) != "1" {
		t.Skip("ALLOW_PAID_MODEL_TEST=1 is required for a live paid provider smoke test")
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	contextPath := strings.TrimSpace(os.Getenv("PORTFOLIO_CONTEXT_FILE"))
	if apiKey == "" || model == "" || contextPath == "" {
		t.Fatal("OPENAI_API_KEY, OPENAI_MODEL, and PORTFOLIO_CONTEXT_FILE are required")
	}

	raw, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := portfoliocontext.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ReasoningView().RunnableItems) != 1 {
		t.Fatalf("live smoke requires exactly one runnable item: %#v", snapshot.ReasoningView().RunnableItems)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	reasoner, err := openaireasoner.New(openaireasoner.Config{
		Endpoint:        strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		Model:           model,
		APIKey:          apiKey,
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := reasoner.Decide(ctx, snapshot.ReasoningView())
	if err != nil {
		t.Fatal(err)
	}
	if err := dotdecision.Validate(snapshot, decision); err != nil {
		t.Fatalf("live provider output failed D3 validation: %v", err)
	}

	if evidencePath := strings.TrimSpace(os.Getenv("DOT_LIVE_PROVIDER_EVIDENCE_FILE")); evidencePath != "" {
		digest, err := dotdurable.DecisionDigest(decision)
		if err != nil {
			t.Fatal(err)
		}
		evidence := map[string]any{
			"state":             "PASS",
			"snapshot_digest":   snapshot.SnapshotDigest,
			"work_item_id":      decision.WorkItemID,
			"kind":              decision.Kind,
			"authority":         decision.RequestedAuthority,
			"decision_digest":   digest,
			"provider":          "openai-responses",
			"model":             model,
			"store":             false,
			"max_output_tokens": 1024,
			"tools":             false,
			"paid_call":         true,
		}
		evidenceRaw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, evidenceRaw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
