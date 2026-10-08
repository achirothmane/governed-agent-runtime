package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

// ToolContract is the model-facing shape of one capability already bound to a
// RunRequest. It deliberately excludes endpoint, credentials, execution IDs,
// and every other authority-bearing field.
type ToolContract struct {
	Name           runtimesdk.ToolName `json:"name"`
	Description    string              `json:"description,omitempty"`
	InputSchema    json.RawMessage     `json:"input_schema"`
	SnapshotDigest string              `json:"-"`
}

func (c ToolContract) Validate() error {
	if strings.TrimSpace(string(c.Name)) == "" {
		return errors.New("tool contract name is required")
	}
	if len(c.InputSchema) == 0 || !json.Valid(c.InputSchema) {
		return errors.New("tool contract input schema must be valid JSON")
	}
	var schema any
	if err := json.Unmarshal(c.InputSchema, &schema); err != nil {
		return fmt.Errorf("decode tool contract input schema: %w", err)
	}
	if _, ok := schema.(map[string]any); !ok {
		return errors.New("tool contract input schema must be a JSON object")
	}
	return nil
}

// ToolContractResolver resolves model-facing schemas for the exact capability
// bindings already present in a run. Implementations must fail closed when a
// remote capability snapshot no longer matches the run binding.
type ToolContractResolver interface {
	ResolveToolContracts(context.Context, runtimesdk.RunRequest) ([]ToolContract, error)
}

func ValidateToolContracts(run runtimesdk.RunRequest, contracts []ToolContract) error {
	if err := run.Validate(); err != nil {
		return err
	}
	if len(contracts) != len(run.Tools) {
		return fmt.Errorf("tool contract count=%d want=%d", len(contracts), len(run.Tools))
	}
	bound := make(map[runtimesdk.ToolName]runtimesdk.ToolDescriptor, len(run.Tools))
	for _, tool := range run.Tools {
		bound[tool.Name] = tool
	}
	seen := make(map[runtimesdk.ToolName]struct{}, len(contracts))
	for _, contract := range contracts {
		if err := contract.Validate(); err != nil {
			return fmt.Errorf("tool contract %q: %w", contract.Name, err)
		}
		tool, ok := bound[contract.Name]
		if !ok {
			return fmt.Errorf("%w: contract for %s", ErrUnboundTool, contract.Name)
		}
		if _, duplicate := seen[contract.Name]; duplicate {
			return fmt.Errorf("duplicate tool contract %q", contract.Name)
		}
		seen[contract.Name] = struct{}{}
		if tool.Protocol == runtimesdk.ToolProtocolMCP && contract.SnapshotDigest != tool.SnapshotDigest {
			return fmt.Errorf("tool contract snapshot mismatch for %s", contract.Name)
		}
	}
	return nil
}
