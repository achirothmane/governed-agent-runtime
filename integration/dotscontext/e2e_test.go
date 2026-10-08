//go:build integration

package dotscontext

import (
	"os"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

func TestPortfolioContextArtifactCanBeConsumedWithoutRepositoryReads(t *testing.T) {
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

	if view.SnapshotDigest == "" || view.StructuralReview != "2026-10-08" {
		t.Fatalf("view=%#v", view)
	}
	if len(view.RunnableItems) != 1 {
		t.Fatalf("runnable items=%#v", view.RunnableItems)
	}
	item := view.RunnableItems[0]
	if item.ID != "dots-model-provider-adapter-005" ||
		item.Project != "portfolio-dot" ||
		item.Authority != "PREPARE" {
		t.Fatalf("runnable item=%#v", item)
	}
	if !snapshot.RequiresHuman("merge") ||
		!snapshot.RequiresHuman("release-or-publication") ||
		!snapshot.RequiresHuman("paid-spend") ||
		snapshot.RequiresHuman("PREPARE") {
		t.Fatalf("human authority=%#v", view.HumanFinalOn)
	}

	// The package receives only the snapshot bytes. It has no repository-path
	// argument or filesystem loader; arbitrary Portfolio files are not available
	// to the reasoning view.
	for _, project := range []string{"portfolio-dot", "data-engine", "governed-agent-runtime"} {
		found := false
		for _, current := range view.NowProjects {
			if current == project {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("NOW project %s absent from %#v", project, view.NowProjects)
		}
	}
}
