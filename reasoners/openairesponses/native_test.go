package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type fakeNativeClient struct {
	out    NativeOutput
	err    error
	calls  int
	params responses.ResponseNewParams
}

func (f *fakeNativeClient) CreateNative(_ context.Context, params responses.ResponseNewParams) (NativeOutput, error) {
	f.calls++
	f.params = params
	return f.out, f.err
}

func nativeTurn() agentloop.Turn {
	r := testRun()
	r.Tools[0].InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"rows":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"price":{"type":"number"}},"required":["id","price"],"additionalProperties":false}},"identity_field":{"type":"string"}},"required":["rows","identity_field"]}`)
	r.Tools = append(r.Tools, runtimesdk.ToolDescriptor{
		Name: "data.delete", Protocol: runtimesdk.ToolProtocolMCP, Endpoint: "https://private-data-engine.example/mcp",
		ReadOnly: false, SnapshotDigest: strings.Repeat("b", 64),
	})
	return agentloop.Turn{Run: r, AgentMission: "Profile raw data", Step: 1}
}

func TestNativeToolCallIsBoundAndSchemaValidated(t *testing.T) {
	f := &fakeNativeClient{out: NativeOutput{Calls: []NativeCall{{
		Name: "cap_0", Arguments: `{"identity_field":"id","rows":[{"id":"a","price":20},{"id":"a","price":21}]}`,
	}}}}
	r, err := NewNativeWithClient(Config{Model: "test-model"}, f)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := r.Decide(context.Background(), nativeTurn())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != agentloop.DecisionTool || decision.Tool != "data.profile" {
		t.Fatalf("decision=%#v", decision)
	}
	if len(decision.Arguments["rows"].([]any)) != 2 {
		t.Fatalf("arguments=%v", decision.Arguments)
	}
	wire, _ := json.Marshal(f.params)
	for _, secret := range []string{"https://private-data-engine.example/mcp", "run-secret-id", "conversation-secret-id", strings.Repeat("a", 64), "data.delete"} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("native request leaked %q", secret)
		}
	}
	for _, required := range []string{"cap_0", "data.profile", "runtime_finish", "runtime_ask", "runtime_fail", "input_schema"} {
		if !strings.Contains(string(wire), required) {
			t.Fatalf("native request missing %q", required)
		}
	}
}

func TestNativeRejectsMalformedBoundToolArguments(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"missing_required", `{"rows":[]}`},
		{"unknown_extra", `{"identity_field":"id","rows":[],"erase":true}`},
		{"wrong_type", `{"identity_field":17,"rows":[]}`},
		{"bad_nested", `{"identity_field":"id","rows":[{"id":"a","price":"wrong"}]}`},
		{"trailing", `{"identity_field":"id","rows":[]},`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeNativeClient{out: NativeOutput{Calls: []NativeCall{{Name: "cap_0", Arguments: tc.args}}}}
			r, _ := NewNativeWithClient(Config{Model: "test"}, f)
			_, err := r.Decide(context.Background(), nativeTurn())
			if !errors.Is(err, agentloop.ErrInvalidDecision) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestNativeRejectsUnboundMultipleAndMixedOutputs(t *testing.T) {
	for _, out := range []NativeOutput{
		{Calls: []NativeCall{{Name: "data.delete", Arguments: "{}"}}},
		{Calls: []NativeCall{{Name: "cap_1", Arguments: "{}"}}},
		{Calls: []NativeCall{{Name: "cap_0", Arguments: "{}"}, {Name: "runtime_finish", Arguments: `{"message":"done"}`}}},
		{Calls: []NativeCall{{Name: "runtime_finish", Arguments: `{"message":"done"}`}}, OtherOutputs: 1},
		{Calls: []NativeCall{{Name: "cap_0", Arguments: "{}", Incomplete: true}}},
		{},
	} {
		f := &fakeNativeClient{out: out}
		r, _ := NewNativeWithClient(Config{Model: "test"}, f)
		if _, err := r.Decide(context.Background(), nativeTurn()); !errors.Is(err, agentloop.ErrInvalidDecision) {
			t.Fatalf("output=%#v err=%v", out, err)
		}
	}
}

func TestNativeTerminalCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind agentloop.DecisionKind
	}{
		{"runtime_finish", agentloop.DecisionFinish}, {"runtime_ask", agentloop.DecisionAsk}, {"runtime_fail", agentloop.DecisionFail},
	} {
		f := &fakeNativeClient{out: NativeOutput{Calls: []NativeCall{{Name: tc.name, Arguments: `{"message":"Evidence conflicts"}`}}}}
		r, _ := NewNativeWithClient(Config{Model: "test"}, f)
		d, err := r.Decide(context.Background(), nativeTurn())
		if err != nil || d.Kind != tc.kind || d.Message != "Evidence conflicts" {
			t.Fatalf("decision=%#v err=%v", d, err)
		}
	}
}

func TestNativeBadContextFailsBeforeProviderCall(t *testing.T) {
	turn := nativeTurn()
	turn.Run.Tools[0].InputSchema = nil
	f := &fakeNativeClient{}
	r, _ := NewNativeWithClient(Config{Model: "test"}, f)
	_, err := r.Decide(context.Background(), turn)
	if !errors.Is(err, agentloop.ErrInvalidReasoningContext) || f.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.calls)
	}
}

func TestNativeOpenAISDKWireFunctionCall(t *testing.T) {
	var request []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" || req.URL.Path != "/responses" {
			t.Errorf("unexpected path %s", req.URL.Path)
		}
		var err error
		request, err = io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_test","object":"response","created_at":1,"status":"completed","model":"test","output":[{"id":"fc_test","type":"function_call","status":"completed","call_id":"call_123","name":"cap_0","arguments":"{\"identity_field\":\"id\",\"rows\":[{\"id\":\"a\",\"price\":20},{\"id\":\"a\",\"price\":21}]}"}]}`)
	}))
	defer server.Close()
	client := openai.NewClient(option.WithUnsafeAllowHTTP(), option.WithBaseURL(server.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0))
	r, _ := NewNativeWithClient(Config{Model: "test"}, nativeSDKClient{client: client})
	d, err := r.Decide(context.Background(), nativeTurn())
	if err != nil || d.Kind != agentloop.DecisionTool || d.Tool != "data.profile" {
		t.Fatalf("decision=%#v err=%v", d, err)
	}
	s := string(request)
	for _, required := range []string{`"tools"`, `"store":false`, `"name":"cap_0"`} {
		if !strings.Contains(s, required) {
			t.Fatalf("missing %q from wire request: %s", required, s)
		}
	}
	for _, secret := range []string{"https://private-data-engine.example/mcp", "data.delete", "run-secret-id"} {
		if strings.Contains(s, secret) {
			t.Fatalf("leaked %q", secret)
		}
	}
}
