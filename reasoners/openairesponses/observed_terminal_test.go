package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
)

func observedTurn() agentloop.Turn {
	turn := nativeTurn()
	turn.Step = 2
	turn.Observations = []agentloop.Observation{{
		Step: 1,
		Tool: "data.profile",
		Result: mcptransport.Result{
			Tool: "data.profile",
			SnapshotDigest: strings.Repeat("a", 64),
			StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"CONFLICTING","reason":"same identity a has price 20 and 21"}`),
		},
	}}
	return turn
}

func TestObservedTerminalPreservesNativeToolFirstAndRequiresTypedFinish(t *testing.T) {
	native := &fakeNativeClient{out: NativeOutput{Calls: []NativeCall{{
		Name: "cap_0",
		Arguments: `{"identity_field":"id","rows":[{"id":"a","price":20},{"id":"a","price":21}]}`,
	}}}}
	terminal := &fakeResponses{output: `{"kind":"FINISH","message":"Two values conflict for the same identity."}`}
	reasoner, err := NewObservedTerminalWithClients(Config{Model: "test-model"}, native, terminal)
	if err != nil {
		t.Fatal(err)
	}
	first := observedTurn()
	first.Step = 1
	first.Observations = nil
	selected, err := reasoner.Decide(context.Background(), first)
	if err != nil || selected.Kind != agentloop.DecisionTool || selected.Tool != "data.profile" {
		t.Fatalf("first decision=%#v err=%v", selected, err)
	}
	next, err := reasoner.Decide(context.Background(), observedTurn())
	if err != nil || next.Kind != agentloop.DecisionFinish {
		t.Fatalf("terminal decision=%#v err=%v", next, err)
	}
	if native.calls != 1 || terminal.calls != 1 {
		t.Fatalf("model calls native=%d terminal=%d", native.calls, terminal.calls)
	}
	encoded, err := json.Marshal(terminal.params)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"a7_observed_terminal_decision", "CONFLICTING", "json_schema"} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("structured terminal omitted %q", required)
		}
	}
	for _, forbidden := range []string{"https://private-data-engine.example/mcp", "run-secret-id", strings.Repeat("a",64)} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("structured terminal leaked %q", forbidden)
		}
	}
}

func TestObservedTerminalFailsClosedOnPlainTextAndWrongDecisions(t *testing.T) {
	for _,tc := range []string{
		"The source rows conflict.",
		`{"kind":"TOOL","message":"data.profile"}`,
		`{"kind":"FINISH","message":""}`,
		`{"kind":"FINISH","message":"yes","tool":"data.delete"}`,
		`{"kind":"FINISH","message":"yes"}{"kind":"FAIL","message":"no"}`,
	} {
		native:=&fakeNativeClient{}
		terminal:=&fakeResponses{output:tc}
		r,_:=NewObservedTerminalWithClients(Config{Model:"test"},native,terminal)
		_,err:=r.Decide(context.Background(),observedTurn())
		if !errors.Is(err,agentloop.ErrInvalidDecision) {
			t.Fatalf("input=%q err=%v",tc,err)
		}
		if native.calls!=0 || terminal.calls!=1 {
			t.Fatalf("unexpected client calls native=%d terminal=%d",native.calls,terminal.calls)
		}
	}
}

func TestObservedTerminalRefusesFinishWithoutSuccessfulEvidence(t *testing.T) {
	for _,modify:=range []func(*agentloop.Turn){
		func(turn *agentloop.Turn){turn.Observations[0].Result.StructuredContent=nil},
		func(turn *agentloop.Turn){turn.Observations[0].Result.IsError=true},
		func(turn *agentloop.Turn){turn.Observations[0].Result.NeedsInput=true},
	} {
		terminal:=&fakeResponses{output:`{"kind":"FINISH","message":"Done."}`}
		r,_:=NewObservedTerminalWithClients(Config{Model:"test"},&fakeNativeClient{},terminal)
		turn:=observedTurn()
		modify(&turn)
		_,err:=r.Decide(context.Background(),turn)
		if !errors.Is(err,agentloop.ErrInvalidDecision) {
			t.Fatalf("unexpected completion: %v",err)
		}
	}
}

func TestObservedTerminalRejectsUnboundOrForgedObservationBeforeProvider(t *testing.T) {
	for _,modify:=range []func(*agentloop.Turn){
		func(turn *agentloop.Turn){turn.Observations[0].Step=3},
		func(turn *agentloop.Turn){turn.Observations[0].Tool="data.delete";turn.Observations[0].Result.Tool="data.delete"},
		func(turn *agentloop.Turn){turn.Observations[0].Result.SnapshotDigest=strings.Repeat("b",64)},
		func(turn *agentloop.Turn){turn.Observations[0].Result.StructuredContent=json.RawMessage("{") },
		func(turn *agentloop.Turn){turn.Observations=append(turn.Observations,turn.Observations[0])},
	} {
		terminal:=&fakeResponses{}
		r,_:=NewObservedTerminalWithClients(Config{Model:"test"},&fakeNativeClient{},terminal)
		turn:=observedTurn()
		modify(&turn)
		_,err:=r.Decide(context.Background(),turn)
		if !errors.Is(err,agentloop.ErrInvalidReasoningContext) || terminal.calls!=0 {
			t.Fatalf("unexpected provider call: err=%v calls=%d",err,terminal.calls)
		}
	}
}
