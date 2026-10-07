package temporaltools

import (
	"context"
	"errors"
	"fmt"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
)

type Activity struct {
	Store   InvocationStore
	Invoker ToolInvoker
}

type ActivityResult struct {
	InvocationID string              `json:"invocation_id"`
	Result       mcptransport.Result `json:"result"`
	ResultDigest string              `json:"result_digest"`
	Reused       bool                `json:"reused"`
}

func (a Activity) Execute(ctx context.Context, ref InvocationRef) (ActivityResult, error) {
	if a.Store == nil {
		return ActivityResult{}, errors.New("tool invocation store is required")
	}
	if a.Invoker == nil {
		return ActivityResult{}, errors.New("tool invoker is required")
	}
	if err := ref.Validate(); err != nil {
		return ActivityResult{}, err
	}

	record, err := a.Store.Get(ctx, ref.InvocationID)
	if err != nil {
		return ActivityResult{}, fmt.Errorf("load invocation: %w", err)
	}
	if !SameBinding(record.Ref, ref) {
		return ActivityResult{}, ErrInvocationConflict
	}
	if record.State == InvocationComplete {
		if record.Result == nil || record.ResultDigest == "" {
			return ActivityResult{}, errors.New("completed invocation has no stored result")
		}
		return ActivityResult{
			InvocationID: ref.InvocationID,
			Result:       *record.Result,
			ResultDigest: record.ResultDigest,
			Reused:       true,
		}, nil
	}

	digest, err := ArgumentsDigest(record.Arguments)
	if err != nil {
		return ActivityResult{}, fmt.Errorf("digest stored arguments: %w", err)
	}
	if digest != ref.ArgumentsDigest {
		return ActivityResult{}, fmt.Errorf("%w: arguments digest mismatch", ErrInvocationConflict)
	}

	result, err := a.Invoker.Invoke(ctx, ref.Tool, record.Arguments)
	if err != nil {
		_ = a.Store.Fail(ctx, ref.InvocationID, err.Error())
		return ActivityResult{}, err
	}
	resultDigest, err := ResultDigest(result)
	if err != nil {
		_ = a.Store.Fail(ctx, ref.InvocationID, err.Error())
		return ActivityResult{}, err
	}
	stored, err := a.Store.Complete(ctx, ref.InvocationID, result, resultDigest)
	if err != nil {
		return ActivityResult{}, err
	}
	if stored.Result == nil || stored.ResultDigest != resultDigest {
		return ActivityResult{}, errors.New("stored invocation result does not match activity result")
	}
	return ActivityResult{
		InvocationID: ref.InvocationID,
		Result:       *stored.Result,
		ResultDigest: stored.ResultDigest,
	}, nil
}
