package sdk

import (
	"errors"
	"fmt"
	"sort"
)

var ErrToolUnavailable = errors.New("required tool unavailable")

type ToolCatalog struct {
	tools map[ToolName]ToolDescriptor
}

func NewToolCatalog(tools []ToolDescriptor) (ToolCatalog, error) {
	catalog := ToolCatalog{tools: make(map[ToolName]ToolDescriptor, len(tools))}
	for _, tool := range tools {
		if err := tool.Validate(); err != nil {
			return ToolCatalog{}, err
		}
		if _, exists := catalog.tools[tool.Name]; exists {
			return ToolCatalog{}, fmt.Errorf("duplicate tool %q", tool.Name)
		}
		catalog.tools[tool.Name] = tool
	}
	return catalog, nil
}

func (c ToolCatalog) Resolve(required []ToolName) ([]ToolDescriptor, error) {
	resolved := make([]ToolDescriptor, 0, len(required))
	seen := make(map[ToolName]struct{}, len(required))
	for _, name := range required {
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("duplicate required tool %q", name)
		}
		seen[name] = struct{}{}
		tool, ok := c.tools[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrToolUnavailable, name)
		}
		resolved = append(resolved, tool)
	}

	// Required tools are ordered canonically before a run is fingerprinted. The
	// agent may declare them in any order without changing execution identity.
	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].Name < resolved[j].Name
	})
	return resolved, nil
}
