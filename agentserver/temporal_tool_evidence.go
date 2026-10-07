package agentserver

import (
	"context"
	"encoding/json"
	"time"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type ToolActivityEvidence struct {
	Store Store
	Clock func() time.Time
}

func (e ToolActivityEvidence) now() time.Time {
	if e.Clock != nil {
		return e.Clock().UTC()
	}
	return time.Now().UTC()
}

func (e ToolActivityEvidence) Called(ctx context.Context, ref temporaltools.InvocationRef) error {
	payload, _ := json.Marshal(toolCallEvidence{
		InvocationID:    ref.InvocationID,
		Tool:            ref.Tool.Name,
		SnapshotDigest:  ref.Tool.SnapshotDigest,
		ArgumentsDigest: ref.ArgumentsDigest,
	})
	_, err := e.Store.AppendEvent(ctx, runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventToolCalled,
		RunID:          ref.RunID,
		ConversationID: ref.ConversationID,
		OccurredAt:     e.now(),
		Payload:        payload,
	})
	return err
}

func (e ToolActivityEvidence) Returned(ctx context.Context, ref temporaltools.InvocationRef, result mcptransport.Result, resultDigest string, reused bool) error {
	payload, _ := json.Marshal(struct {
		InvocationID   string              `json:"invocation_id"`
		Tool           runtimesdk.ToolName `json:"tool"`
		SnapshotDigest string              `json:"snapshot_digest"`
		IsError        bool                `json:"is_error"`
		ResultDigest   string              `json:"result_digest"`
		Reused         bool                `json:"reused"`
	}{
		InvocationID:   ref.InvocationID,
		Tool:           ref.Tool.Name,
		SnapshotDigest: ref.Tool.SnapshotDigest,
		IsError:        result.IsError,
		ResultDigest:   resultDigest,
		Reused:         reused,
	})
	_, err := e.Store.AppendEvent(ctx, runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventToolReturned,
		RunID:          ref.RunID,
		ConversationID: ref.ConversationID,
		OccurredAt:     e.now(),
		Payload:        payload,
	})
	return err
}

func (e ToolActivityEvidence) Failed(ctx context.Context, ref temporaltools.InvocationRef, message string) error {
	payload, _ := json.Marshal(toolFailureEvidence{
		InvocationID:   ref.InvocationID,
		Tool:           ref.Tool.Name,
		SnapshotDigest: ref.Tool.SnapshotDigest,
		Error:          message,
	})
	_, err := e.Store.AppendEvent(ctx, runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventToolFailed,
		RunID:          ref.RunID,
		ConversationID: ref.ConversationID,
		OccurredAt:     e.now(),
		Payload:        payload,
	})
	return err
}
