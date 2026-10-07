package temporalbackend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	serviceerror "go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

const (
	memoFingerprintKey = "ai_native_runtime_fingerprint"
	memoRunIDKey       = "ai_native_runtime_run_id"
)

var (
	ErrUnprovenBinding = errors.New("temporal execution binding cannot be proven")
	ErrBindingMismatch = errors.New("temporal execution binding mismatch")
)

// Client is the narrow Temporal surface required by the runtime backend.
// client.Client satisfies this interface.
type Client interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, any, ...any) (client.WorkflowRun, error)
	DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	SignalWorkflow(context.Context, string, string, string, any) error
	CancelWorkflow(context.Context, string, string) error
}

// WorkflowInput is the immutable payload handed to the registered Temporal workflow.
// Fingerprint binds the workflow execution to the exact runtime request.
type WorkflowInput struct {
	Request     runtimesdk.RunRequest `json:"request"`
	Fingerprint string                `json:"fingerprint"`
}

type Backend struct {
	Client        Client
	TaskQueue     string
	Workflow      any
	DataConverter converter.DataConverter
}

func (b Backend) Name() string { return "temporal" }

func (b Backend) validate() error {
	if b.Client == nil {
		return errors.New("temporal client is required")
	}
	if strings.TrimSpace(b.TaskQueue) == "" {
		return errors.New("temporal task queue is required")
	}
	if b.Workflow == nil {
		return errors.New("temporal workflow is required")
	}
	return nil
}

func (b Backend) dataConverter() converter.DataConverter {
	if b.DataConverter != nil {
		return b.DataConverter
	}
	return converter.GetDefaultDataConverter()
}

func (b Backend) Start(ctx context.Context, run runtimesdk.RunRequest) (runtimesdk.RunHandle, error) {
	if err := b.validate(); err != nil {
		return runtimesdk.RunHandle{}, err
	}
	fingerprint, err := run.Fingerprint()
	if err != nil {
		return runtimesdk.RunHandle{}, fmt.Errorf("fingerprint run: %w", err)
	}

	options := client.StartWorkflowOptions{
		ID:                                       string(run.ID),
		TaskQueue:                                b.TaskQueue,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		Memo: map[string]any{
			memoFingerprintKey: fingerprint,
			memoRunIDKey:       string(run.ID),
		},
	}
	input := WorkflowInput{Request: run, Fingerprint: fingerprint}
	started, err := b.Client.ExecuteWorkflow(ctx, options, b.Workflow, input)
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			existing, inspectErr := b.Inspect(ctx, run.ID)
			if inspectErr != nil {
				return runtimesdk.RunHandle{}, fmt.Errorf("reconcile already-started temporal run: %w", inspectErr)
			}
			if existing.Fingerprint != fingerprint {
				return runtimesdk.RunHandle{}, fmt.Errorf("%w: existing=%s requested=%s", ErrBindingMismatch, existing.Fingerprint, fingerprint)
			}
			return existing, nil
		}
		return runtimesdk.RunHandle{}, fmt.Errorf("start temporal workflow: %w", err)
	}
	if started == nil || strings.TrimSpace(started.GetRunID()) == "" {
		return runtimesdk.RunHandle{}, errors.New("temporal returned an empty workflow run id")
	}

	return runtimesdk.RunHandle{
		ID:          run.ID,
		Backend:     b.Name(),
		ExternalID:  started.GetRunID(),
		Fingerprint: fingerprint,
		State:       runtimesdk.RunQueued,
	}, nil
}

func (b Backend) Inspect(ctx context.Context, id runtimesdk.RunID) (runtimesdk.RunHandle, error) {
	if err := b.validate(); err != nil {
		return runtimesdk.RunHandle{}, err
	}
	if strings.TrimSpace(string(id)) == "" {
		return runtimesdk.RunHandle{}, errors.New("run id is required")
	}

	// Empty Temporal run ID intentionally targets the latest execution for this
	// workflow ID. Reuse is rejected on Start, so this cannot silently select a
	// later unrelated execution created through this backend.
	description, err := b.Client.DescribeWorkflowExecution(ctx, string(id), "")
	if err != nil {
		return runtimesdk.RunHandle{ID: id, Backend: b.Name(), State: runtimesdk.RunUnknown}, fmt.Errorf("describe temporal workflow: %w", err)
	}
	info := description.GetWorkflowExecutionInfo()
	if info == nil || info.GetExecution() == nil {
		return runtimesdk.RunHandle{ID: id, Backend: b.Name(), State: runtimesdk.RunUnknown}, ErrUnprovenBinding
	}

	fingerprint, boundRunID, err := b.decodeBinding(info.GetMemo())
	if err != nil {
		return runtimesdk.RunHandle{
			ID:         id,
			Backend:    b.Name(),
			ExternalID: info.GetExecution().GetRunId(),
			State:      runtimesdk.RunUnknown,
		}, err
	}
	if boundRunID != string(id) {
		return runtimesdk.RunHandle{
			ID:          id,
			Backend:     b.Name(),
			ExternalID:  info.GetExecution().GetRunId(),
			Fingerprint: fingerprint,
			State:       runtimesdk.RunUnknown,
		}, fmt.Errorf("%w: memo run id=%s requested=%s", ErrBindingMismatch, boundRunID, id)
	}

	return runtimesdk.RunHandle{
		ID:          id,
		Backend:     b.Name(),
		ExternalID:  info.GetExecution().GetRunId(),
		Fingerprint: fingerprint,
		State:       mapStatus(info.GetStatus()),
	}, nil
}

func (b Backend) Signal(ctx context.Context, id runtimesdk.RunID, name string, payload []byte) error {
	if err := b.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(string(id)) == "" {
		return errors.New("run id is required")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("signal name is required")
	}
	if err := b.Client.SignalWorkflow(ctx, string(id), "", name, payload); err != nil {
		return fmt.Errorf("signal temporal workflow: %w", err)
	}
	return nil
}

func (b Backend) Cancel(ctx context.Context, id runtimesdk.RunID, reason string) error {
	if err := b.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(string(id)) == "" {
		return errors.New("run id is required")
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("cancellation reason is required")
	}
	// Temporal's cancellation API does not persist a reason field. Callers retain
	// the reason in the runtime/audit event stream; this adapter performs the
	// cancellation against the latest execution in the bound workflow chain.
	if err := b.Client.CancelWorkflow(ctx, string(id), ""); err != nil {
		return fmt.Errorf("cancel temporal workflow: %w", err)
	}
	return nil
}

func (b Backend) decodeBinding(memo anyMemo) (string, string, error) {
	if memo == nil || len(memo.GetFields()) == 0 {
		return "", "", ErrUnprovenBinding
	}
	fpPayload, ok := memo.GetFields()[memoFingerprintKey]
	if !ok || fpPayload == nil {
		return "", "", ErrUnprovenBinding
	}
	runPayload, ok := memo.GetFields()[memoRunIDKey]
	if !ok || runPayload == nil {
		return "", "", ErrUnprovenBinding
	}

	var fingerprint string
	if err := b.dataConverter().FromPayload(fpPayload, &fingerprint); err != nil {
		return "", "", fmt.Errorf("%w: decode fingerprint memo: %v", ErrUnprovenBinding, err)
	}
	var runID string
	if err := b.dataConverter().FromPayload(runPayload, &runID); err != nil {
		return "", "", fmt.Errorf("%w: decode run id memo: %v", ErrUnprovenBinding, err)
	}
	if strings.TrimSpace(fingerprint) == "" || strings.TrimSpace(runID) == "" {
		return "", "", ErrUnprovenBinding
	}
	return fingerprint, runID, nil
}

// anyMemo keeps decodeBinding testable without making the backend depend on a
// concrete protobuf memo type in its signature.
type anyMemo interface {
	GetFields() map[string]*commonPayload
}

type commonPayload = commonpb.Payload

func mapStatus(status enumspb.WorkflowExecutionStatus) runtimesdk.RunState {
	switch status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING:
		return runtimesdk.RunRunning
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return runtimesdk.RunSucceeded
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
		enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return runtimesdk.RunFailed
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return runtimesdk.RunCanceled
	case enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		// A continued-as-new predecessor is not proof of the current chain state.
		return runtimesdk.RunUnknown
	default:
		return runtimesdk.RunUnknown
	}
}
