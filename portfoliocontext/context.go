package portfoliocontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidSnapshot = errors.New("invalid portfolio context snapshot")
	ErrDigestMismatch  = errors.New("portfolio context snapshot digest mismatch")
	ErrNonExecutable   = errors.New("portfolio context snapshot is non-executable")
)

type SourceBinding struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type ProjectState struct {
	ID           string `json:"id"`
	Freshness    string `json:"freshness"`
	Completeness string `json:"completeness"`
}

type RunnableItem struct {
	ID               string   `json:"id"`
	Project          string   `json:"project"`
	Authority        string   `json:"authority"`
	Objective        string   `json:"objective"`
	EvidenceRequired []string `json:"evidence_required"`
	StopConditions   []string `json:"stop_conditions"`
}

type ContractEdge struct {
	ID             string   `json:"id"`
	Source         string   `json:"source"`
	Target         string   `json:"target"`
	Relations      []string `json:"relations"`
	HardDependency bool     `json:"hard_dependency"`
}

type WIP struct {
	NowCap    int `json:"now_cap"`
	NowCount  int `json:"now_count"`
	NextCap   int `json:"next_cap"`
	NextCount int `json:"next_count"`
}

type Projection struct {
	StructuralReview   string         `json:"structural_review"`
	NowProjects        []string       `json:"now_projects"`
	NowProjectState    []ProjectState `json:"now_project_state"`
	RunnableItems      []RunnableItem `json:"runnable_items"`
	VerifiedContracts  []ContractEdge `json:"verified_contracts"`
	HumanFinalOn       []string       `json:"human_final_on"`
	ExecutionPrinciple string         `json:"execution_principle"`
	WIP                WIP            `json:"wip"`
}

type Snapshot struct {
	SchemaVersion  int             `json:"schema_version"`
	State          string          `json:"state"`
	Reasons        []string        `json:"reasons"`
	SourceBindings []SourceBinding `json:"source_bindings"`
	Projection     Projection      `json:"projection"`
	SnapshotDigest string          `json:"snapshot_digest"`
}

type ReasoningView struct {
	SnapshotDigest     string
	StructuralReview   string
	NowProjects        []string
	NowProjectState    []ProjectState
	RunnableItems      []RunnableItem
	VerifiedContracts  []ContractEdge
	HumanFinalOn       []string
	ExecutionPrinciple string
	WIP                WIP
}

func Parse(raw []byte) (Snapshot, error) {
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return Snapshot{}, fmt.Errorf("%w: decode: %v", ErrInvalidSnapshot, err)
	}
	claimed, ok := generic["snapshot_digest"].(string)
	if !ok || len(claimed) != 64 {
		return Snapshot{}, fmt.Errorf("%w: snapshot_digest missing or malformed", ErrInvalidSnapshot)
	}
	delete(generic, "snapshot_digest")
	canonical, err := json.Marshal(generic)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: canonicalize: %v", ErrInvalidSnapshot, err)
	}
	sum := sha256.Sum256(canonical)
	actual := hex.EncodeToString(sum[:])
	if actual != claimed {
		return Snapshot{}, fmt.Errorf("%w: claimed=%s actual=%s", ErrDigestMismatch, claimed, actual)
	}

	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: typed decode: %v", ErrInvalidSnapshot, err)
	}
	if err := validate(snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.State != "EXECUTABLE" {
		return Snapshot{}, fmt.Errorf("%w: reasons=%v", ErrNonExecutable, snapshot.Reasons)
	}
	return snapshot, nil
}

func validate(snapshot Snapshot) error {
	if snapshot.SchemaVersion != 1 {
		return fmt.Errorf("%w: unsupported schema_version %d", ErrInvalidSnapshot, snapshot.SchemaVersion)
	}
	if snapshot.State != "EXECUTABLE" && snapshot.State != "NON_EXECUTABLE" {
		return fmt.Errorf("%w: invalid state %q", ErrInvalidSnapshot, snapshot.State)
	}
	if strings.TrimSpace(snapshot.Projection.StructuralReview) == "" ||
		strings.TrimSpace(snapshot.Projection.StructuralReview) == "UNKNOWN" {
		return fmt.Errorf("%w: structural review missing", ErrInvalidSnapshot)
	}
	if len(snapshot.SourceBindings) == 0 {
		return fmt.Errorf("%w: source bindings missing", ErrInvalidSnapshot)
	}
	for _, binding := range snapshot.SourceBindings {
		if strings.TrimSpace(binding.Path) == "" || len(binding.SHA256) != 64 {
			return fmt.Errorf("%w: malformed source binding %#v", ErrInvalidSnapshot, binding)
		}
	}
	if len(snapshot.Projection.HumanFinalOn) == 0 {
		return fmt.Errorf("%w: human authority boundary missing", ErrInvalidSnapshot)
	}
	for _, item := range snapshot.Projection.RunnableItems {
		if strings.TrimSpace(item.ID) == "" ||
			strings.TrimSpace(item.Project) == "" ||
			strings.TrimSpace(item.Authority) == "" ||
			strings.TrimSpace(item.Objective) == "" ||
			len(item.EvidenceRequired) == 0 ||
			len(item.StopConditions) == 0 {
			return fmt.Errorf("%w: incomplete runnable item %#v", ErrInvalidSnapshot, item)
		}
	}
	return nil
}

func (s Snapshot) ReasoningView() ReasoningView {
	return ReasoningView{
		SnapshotDigest:     s.SnapshotDigest,
		StructuralReview:   s.Projection.StructuralReview,
		NowProjects:        append([]string(nil), s.Projection.NowProjects...),
		NowProjectState:    append([]ProjectState(nil), s.Projection.NowProjectState...),
		RunnableItems:      append([]RunnableItem(nil), s.Projection.RunnableItems...),
		VerifiedContracts:  append([]ContractEdge(nil), s.Projection.VerifiedContracts...),
		HumanFinalOn:       append([]string(nil), s.Projection.HumanFinalOn...),
		ExecutionPrinciple: s.Projection.ExecutionPrinciple,
		WIP:                s.Projection.WIP,
	}
}

func (s Snapshot) RequiresHuman(action string) bool {
	action = strings.TrimSpace(action)
	for _, value := range s.Projection.HumanFinalOn {
		if value == action {
			return true
		}
	}
	return false
}
