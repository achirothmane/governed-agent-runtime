//go:build integration

package dotsdecision

import (
	"os"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

func TestCurrentPortfolioSnapshotProducesBoundedNextGateProposal(t *testing.T) {
	path := os.Getenv("PORTFOLIO_CONTEXT_FILE")
	if path == "" {
		t.Skip("PORTFOLIO_CONTEXT_FILE is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := portfoliocontext.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	view := snapshot.ReasoningView()
	if len(view.RunnableItems) != 1 {
		t.Fatalf("runnable=%#v", view.RunnableItems)
	}
	item := view.RunnableItems[0]
	if item.ID != "dots-decision-contract-003" {
		t.Fatalf("current READY item=%#v", item)
	}

	decision := dotdecision.Decision{
		Kind:               dotdecision.ProposeNextGate,
		SnapshotDigest:     view.SnapshotDigest,
		WorkItemID:         item.ID,
		RequestedAuthority: item.Authority,
		RequestedAction:    "prepare-decision-contract",
		Rationale:          "Current sealed context admits exactly one PREPARE work item with explicit evidence and stop conditions.",
	}
	if err := dotdecision.Validate(snapshot, decision); err != nil {
		t.Fatal(err)
	}

	// A model may propose this, but the validator must reject it before any executor sees it.
	decision.RequestedAuthority = "COMMIT_EXTERNAL"
	if err := dotdecision.Validate(snapshot, decision); err == nil {
		t.Fatal("authority escalation was accepted")
	}

	decision.RequestedAuthority = item.Authority
	decision.RequestedAction = "merge"
	decision.Kind = dotdecision.ProposeNextGate
	if err := dotdecision.Validate(snapshot, decision); err == nil {
		t.Fatal("human-final merge was accepted as ordinary proposal")
	}

	decision.Kind = dotdecision.AskHuman
	if err := dotdecision.Validate(snapshot, decision); err != nil {
		t.Fatal(err)
	}
}
