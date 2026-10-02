package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/achirothmane/governed-agent-runtime/internal/cognition"
)

func NewPlanBinding(plan cognition.Plan) (PlanBinding, error) {
	document, err := json.Marshal(plan)
	if err != nil {
		return PlanBinding{}, fmt.Errorf("marshal plan: %w", err)
	}

	sum := sha256.Sum256(document)
	binding := PlanBinding{
		PlanID:   plan.ID,
		AgentID:  plan.AgentID,
		EventID:  plan.EventID,
		Digest:   append([]byte(nil), sum[:]...),
		Document: append(json.RawMessage(nil), document...),
	}
	if err := binding.Validate(); err != nil {
		return PlanBinding{}, err
	}
	return binding, nil
}
