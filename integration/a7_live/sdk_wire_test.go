//go:build integration

package a7live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	openairesponses "github.com/achirothmane/governed-agent-runtime/reasoners/openairesponses"
)

// Real Ollama through the official SDK, with a same-run loopback capture proxy.
// The capture is restricted to the synthetic fixture's REQUEST JSON (not
// credentials, endpoints, provider responses or internal invocation state).
// The independent Python comparator checks this against direct HTTP semantics.
func TestActualLocalSDKNativeWireFirstStep(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("A7_LIVE_MODEL_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("A7_LIVE_MODEL"))
	if base == "" || model == "" {
		t.Skip("A7 local model env vars required")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") && !strings.HasPrefix(base, "http://localhost:") {
		t.Fatal("only loopback Ollama endpoints are allowed")
	}
	base = strings.TrimRight(base, "/")
	upstream := &http.Client{Timeout: 120 * time.Second}
	var captured []byte
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/responses" {
			http.Error(w, "unsupported test endpoint", http.StatusNotFound)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			http.Error(w, "could not read SDK wire", http.StatusBadRequest)
			return
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			http.Error(w, "invalid SDK JSON", http.StatusBadRequest)
			return
		}
		// Never write API credentials to a fixture, even local fake tokens.
		if _, hasKey := parsed["api_key"]; hasKey {
			http.Error(w, "unexpected API key in request JSON", http.StatusBadRequest)
			return
		}
		captured = append([]byte(nil), raw...)
		forward, err := http.NewRequestWithContext(req.Context(), http.MethodPost, base+"/responses", bytes.NewReader(raw))
		if err != nil {
			http.Error(w, "upstream request failed", http.StatusInternalServerError)
			return
		}
		forward.Header.Set("Content-Type", "application/json")
		resp, err := upstream.Do(forward)
		if err != nil {
			http.Error(w, "local Ollama unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
	}))
	defer proxy.Close()

	client := openai.NewClient(
		option.WithUnsafeAllowHTTP(),
		option.WithBaseURL(proxy.URL),
		option.WithAPIKey("local-no-remote-secret"),
		option.WithMaxRetries(0),
		// Experimental provider control. No seed: unsupported in Ollama 0.13.3
		// Responses compatibility docs. Do not alter production Reasoner.
		option.WithJSONSet("temperature", float64(0)),
	)
	reasoner, err := openairesponses.NewNativeWithClient(
		openairesponses.Config{Model: model, MaxOutputTokens: 768},
		localNativeResponseClient{client: client},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	start := time.Now()
	decision, decideErr := reasoner.Decide(ctx, agentloop.Turn{
		Run: smokeRun(),
		AgentMission: "Use data.profile to inspect raw evidence before answering; never guess results.",
		Step: 1,
	})
	if len(captured) == 0 {
		t.Fatalf("SDK did not emit request; error=%v", decideErr)
	}
	// Synthetic model prompt only; no user data, provider text, internal IDs,
	// provider auth header or MCP service credentials are stored.
	if err := os.WriteFile("a7-sdk-wire.json", captured, 0600); err != nil {
		t.Fatalf("write synthetic SDK request fixture: %v", err)
	}
	t.Logf("SDK_NATIVE_FIRST_STEP: model=%s temperature=0 latency=%s kind=%s accepted=%t",
		model, time.Since(start).Round(time.Millisecond), decision.Kind,
		decideErr == nil && decision.Kind == agentloop.DecisionTool && decision.Tool == "data.profile")
	if decideErr != nil {
		t.Fatalf("actual local SDK-native decision rejected (captured synthetic request for comparison): %v", decideErr)
	}
	if decision.Kind != agentloop.DecisionTool || decision.Tool != "data.profile" ||
		decision.Arguments["identity_field"] != "id" {
		t.Fatal(fmt.Sprintf("unexpected tool selection: kind=%s tool=%s", decision.Kind, decision.Tool))
	}
	rows, ok := decision.Arguments["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("wrong number of proposed source rows: %v", decision.Arguments["rows"])
	}
}
