package releaseengineer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
	"github.com/achirothmane/governed-agent-runtime/internal/cognition"
	"github.com/achirothmane/governed-agent-runtime/internal/effects/githubpr"
	"github.com/achirothmane/governed-agent-runtime/internal/governance"
	agentruntime "github.com/achirothmane/governed-agent-runtime/internal/runtime"
)

const (
	ToolOpenPullRequest   agent.ToolRef = "github.open_pull_request"
	ActionOpenPullRequest               = "open_pull_request"
	AdmissionProfile                    = "github-pr-effect-v1"
)

var (
	ErrUnsupportedPlan = errors.New("release engineer plan is not a supported GitHub PR effect")
	ErrEffectUnresolved = errors.New("release engineer GitHub effect remains unresolved")
	ErrEffectDenied = errors.New("release engineer GitHub effect was denied")
)

type GitHubBoundary interface {
	Observe(context.Context, githubpr.Spec) (githubpr.Observation, error)
	ExecuteGuarded(context.Context, githubpr.Spec) (githubpr.Execution, error)
}

type Handler struct {
	Store     agentruntime.Store
	Admission *governance.Client
	GitHub    GitHubBoundary
	Clock     func() time.Time
}

type pullArguments struct {
	Owner           string `json:"owner"`
	Repo            string `json:"repo"`
	Head            string `json:"head"`
	Base            string `json:"base"`
	ExpectedHeadSHA string `json:"expected_head_sha"`
	Title           string `json:"title"`
	Body            string `json:"body,omitempty"`
	Draft           bool   `json:"draft,omitempty"`
}

func (h Handler) Handle(ctx context.Context, work agentruntime.ClaimedWork) error {
	if h.Store == nil || h.Admission == nil || h.GitHub == nil {
		return errors.New("release engineer handler requires store, admission client, and GitHub boundary")
	}
	now := time.Now
	if h.Clock != nil {
		now = h.Clock
	}

	record, err := h.Store.Get(ctx, work.Lease.EventID)
	if err != nil {
		return fmt.Errorf("load release engineer work: %w", err)
	}
	spec, effectID, err := specFromRecord(record)
	if err != nil {
		return err
	}

	switch record.LifecycleState {
	case agent.StateWaitingForAdmission:
		return h.beginAndExecute(ctx, work.Lease, spec, effectID, now)
	case agent.StateUnknown:
		return h.reconcileTakeover(ctx, work.Lease, record, spec, effectID, now)
	default:
		return fmt.Errorf("%w: lifecycle state %s", ErrUnsupportedPlan, record.LifecycleState)
	}
}

func (h Handler) reconcileTakeover(
	ctx context.Context,
	lease agentruntime.LeaseToken,
	record agentruntime.WorkRecord,
	spec githubpr.Spec,
	effectID string,
	now func() time.Time,
) error {
	observedAt := now().UTC()
	observation, err := h.GitHub.Observe(ctx, spec)
	if err != nil {
		return fmt.Errorf("%w: observe UNKNOWN effect: %v", ErrEffectUnresolved, err)
	}

	switch observation.Kind {
	case githubpr.ObservationAppliedOnce:
		proof, err := resolution(record, lease, effectID, agentruntime.EffectAppliedOnce, observation, observedAt)
		if err != nil {
			return err
		}
		if _, err := h.Store.ResolveUnknown(ctx, lease, proof, observedAt); err != nil {
			return fmt.Errorf("persist applied effect reconciliation: %w", err)
		}
		return h.finishVerification(ctx, lease, now)
	case githubpr.ObservationAbsent:
		proof, err := resolution(record, lease, effectID, agentruntime.EffectAbsent, observation, observedAt)
		if err != nil {
			return err
		}
		if _, err := h.Store.ResolveUnknown(ctx, lease, proof, observedAt); err != nil {
			return fmt.Errorf("persist absent effect reconciliation: %w", err)
		}
		// The old worker's admission is never reused. ResolveUnknown only reopens
		// WAITING_FOR_ADMISSION; BeginRemoteExecution obtains authority bound to
		// this worker and this lease epoch.
		return h.beginAndExecute(ctx, lease, spec, effectID, now)
	case githubpr.ObservationUnknown, githubpr.ObservationDivergent:
		return fmt.Errorf("%w: recovery observation=%s reason=%s", ErrEffectUnresolved, observation.Kind, observation.Reason)
	default:
		return fmt.Errorf("%w: unsupported recovery observation %q", ErrEffectUnresolved, observation.Kind)
	}
}

func (h Handler) beginAndExecute(
	ctx context.Context,
	lease agentruntime.LeaseToken,
	spec githubpr.Spec,
	effectID string,
	now func() time.Time,
) error {
	executing, err := agentruntime.BeginRemoteExecution(ctx, h.Store, lease, h.Admission, now().UTC())
	if err != nil {
		return fmt.Errorf("begin admitted GitHub effect: %w", err)
	}
	if err := bindAdmission(executing, spec); err != nil {
		_, _ = h.Store.Transition(ctx, lease, agent.StateRevoked, now().UTC())
		return err
	}

	before, err := h.GitHub.Observe(ctx, spec)
	if err != nil {
		_, _ = h.Store.Transition(ctx, lease, agent.StateUnknown, now().UTC())
		return fmt.Errorf("%w: pre-dispatch observation failed: %v", ErrEffectUnresolved, err)
	}
	switch before.Kind {
	case githubpr.ObservationAppliedOnce:
		return h.finishFromExecuting(ctx, lease, now)
	case githubpr.ObservationAbsent:
		// proceed
	case githubpr.ObservationUnknown, githubpr.ObservationDivergent:
		_, _ = h.Store.Transition(ctx, lease, agent.StateUnknown, now().UTC())
		return fmt.Errorf("%w: pre-dispatch observation=%s reason=%s", ErrEffectUnresolved, before.Kind, before.Reason)
	default:
		_, _ = h.Store.Transition(ctx, lease, agent.StateUnknown, now().UTC())
		return fmt.Errorf("%w: unsupported pre-dispatch observation %q", ErrEffectUnresolved, before.Kind)
	}

	dispatched, dispatchErr := h.GitHub.ExecuteGuarded(ctx, spec)
	if dispatchErr != nil {
		return h.reconcileAfterDispatch(ctx, lease, spec, effectID, now, dispatchErr)
	}
	if dispatched.Kind == githubpr.ExecutionDenied {
		_, _ = h.Store.Transition(ctx, lease, agent.StateRevoked, now().UTC())
		return fmt.Errorf("%w: %s", ErrEffectDenied, dispatched.Reason)
	}
	if dispatched.Kind != githubpr.ExecutionDispatched {
		_, _ = h.Store.Transition(ctx, lease, agent.StateUnknown, now().UTC())
		return fmt.Errorf("%w: unsupported dispatch result %q", ErrEffectUnresolved, dispatched.Kind)
	}

	return h.reconcileAfterDispatch(ctx, lease, spec, effectID, now, nil)
}

func (h Handler) reconcileAfterDispatch(
	ctx context.Context,
	lease agentruntime.LeaseToken,
	spec githubpr.Spec,
	effectID string,
	now func() time.Time,
	dispatchErr error,
) error {
	observation, observeErr := h.GitHub.Observe(ctx, spec)
	if observeErr == nil && observation.Kind == githubpr.ObservationAppliedOnce {
		return h.finishFromExecuting(ctx, lease, now)
	}

	_, _ = h.Store.Transition(ctx, lease, agent.StateUnknown, now().UTC())
	if observeErr != nil {
		return fmt.Errorf("%w: dispatch=%v observe=%v", ErrEffectUnresolved, dispatchErr, observeErr)
	}
	return fmt.Errorf(
		"%w: effect=%s dispatch=%v observation=%s reason=%s",
		ErrEffectUnresolved,
		effectID,
		dispatchErr,
		observation.Kind,
		observation.Reason,
	)
}

func (h Handler) finishFromExecuting(ctx context.Context, lease agentruntime.LeaseToken, now func() time.Time) error {
	if _, err := h.Store.Transition(ctx, lease, agent.StateVerifying, now().UTC()); err != nil {
		return fmt.Errorf("enter effect verification: %w", err)
	}
	return h.finishVerification(ctx, lease, now)
}

func (h Handler) finishVerification(ctx context.Context, lease agentruntime.LeaseToken, now func() time.Time) error {
	if _, err := h.Store.Transition(ctx, lease, agent.StateSleeping, now().UTC()); err != nil {
		return fmt.Errorf("finish effect verification: %w", err)
	}
	return nil
}

func specFromRecord(record agentruntime.WorkRecord) (githubpr.Spec, string, error) {
	if record.Plan == nil {
		return githubpr.Spec{}, "", agentruntime.ErrPlanRequired
	}
	var plan cognition.Plan
	if err := json.Unmarshal(record.Plan.Document, &plan); err != nil {
		return githubpr.Spec{}, "", fmt.Errorf("%w: decode durable plan: %v", ErrUnsupportedPlan, err)
	}
	if plan.AgentID != record.Event.AgentID || plan.EventID != record.Event.ID || len(plan.Effects) != 1 {
		return githubpr.Spec{}, "", ErrUnsupportedPlan
	}
	effect := plan.Effects[0]
	if effect.Tool != ToolOpenPullRequest || effect.Action != ActionOpenPullRequest {
		return githubpr.Spec{}, "", ErrUnsupportedPlan
	}
	var args pullArguments
	if err := strictJSON(effect.Arguments, &args); err != nil {
		return githubpr.Spec{}, "", fmt.Errorf("%w: arguments: %v", ErrUnsupportedPlan, err)
	}
	effectID := record.Event.ID + ":" + effect.ID
	spec := githubpr.Spec{
		EffectID:        effectID,
		Owner:           args.Owner,
		Repo:            args.Repo,
		Head:            args.Head,
		Base:            args.Base,
		ExpectedHeadSHA: args.ExpectedHeadSHA,
		Title:           args.Title,
		Body:            args.Body,
		Draft:           args.Draft,
	}
	if err := spec.Validate(); err != nil {
		return githubpr.Spec{}, "", fmt.Errorf("%w: %v", ErrUnsupportedPlan, err)
	}
	return spec, effectID, nil
}

func bindAdmission(record agentruntime.WorkRecord, spec githubpr.Spec) error {
	if record.Admission == nil {
		return governance.ErrAdmission
	}
	var admission governance.Admission
	if err := json.Unmarshal(record.Admission.Document, &admission); err != nil {
		return governance.ErrAdmission
	}
	wantTarget := "github:" + strings.ToLower(spec.Owner+"/"+spec.Repo)
	if strings.ToLower(admission.Target) != wantTarget || admission.Profile != AdmissionProfile {
		return fmt.Errorf(
			"%w: admission target/profile %q/%q does not bind GitHub effect %q/%q",
			governance.ErrAdmission,
			admission.Target,
			admission.Profile,
			wantTarget,
			AdmissionProfile,
		)
	}
	return nil
}

func resolution(
	record agentruntime.WorkRecord,
	lease agentruntime.LeaseToken,
	effectID string,
	outcome agentruntime.EffectResolutionOutcome,
	observation githubpr.Observation,
	observedAt time.Time,
) (agentruntime.EffectResolution, error) {
	if record.Plan == nil {
		return agentruntime.EffectResolution{}, agentruntime.ErrPlanRequired
	}
	evidence, err := json.Marshal(observation)
	if err != nil {
		return agentruntime.EffectResolution{}, err
	}
	sum := sha256.Sum256(evidence)
	return agentruntime.EffectResolution{
		EffectID:       effectID,
		Outcome:        outcome,
		WorkerID:       lease.WorkerID,
		LeaseEpoch:     lease.Epoch,
		PlanDigest:     hex.EncodeToString(record.Plan.Digest),
		EvidenceDigest: hex.EncodeToString(sum[:]),
		ObservedAt:     observedAt.UTC(),
	}, nil
}

func strictJSON(document []byte, target any) error {
	if len(document) == 0 {
		return errors.New("arguments are required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
