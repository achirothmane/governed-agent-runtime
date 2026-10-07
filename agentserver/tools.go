package agentserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type toolInvokeRequest struct {
	InvocationID string         `json:"invocation_id,omitempty"`
	Arguments    map[string]any `json:"arguments"`
}

type toolInvokeResponse struct {
	Result         mcptransport.Result `json:"result"`
	EventPersisted bool                `json:"event_persisted"`
}

type toolCallEvidence struct {
	InvocationID    string              `json:"invocation_id,omitempty"`
	Tool            runtimesdk.ToolName `json:"tool"`
	SnapshotDigest  string              `json:"snapshot_digest"`
	ArgumentsDigest string              `json:"arguments_digest"`
}

type toolResultEvidence struct {
	InvocationID   string              `json:"invocation_id,omitempty"`
	Tool           runtimesdk.ToolName `json:"tool"`
	SnapshotDigest string              `json:"snapshot_digest"`
	IsError        bool                `json:"is_error"`
	ResultDigest   string              `json:"result_digest"`
}

type toolFailureEvidence struct {
	InvocationID   string              `json:"invocation_id,omitempty"`
	Tool           runtimesdk.ToolName `json:"tool"`
	SnapshotDigest string              `json:"snapshot_digest"`
	Error          string              `json:"error"`
}

func (s *Service) handleInvokeTool(w http.ResponseWriter, r *http.Request) {
	if s.invoker == nil && s.durableInvoker == nil {
		writeError(w, http.StatusNotImplemented, "tool_invocation_unavailable", "server has no tool invocation boundary")
		return
	}

	runID := runtimesdk.RunID(strings.TrimSpace(r.PathValue("runID")))
	toolName := runtimesdk.ToolName(strings.TrimSpace(r.PathValue("tool")))
	if runID == "" || toolName == "" {
		writeError(w, http.StatusBadRequest, "invalid_tool_request", "run id and tool name are required")
		return
	}

	record, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	runtime, err := s.runtimeForRun(r.Context(), record)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}

	bound, fingerprint, err := runtime.Bind(runtimesdk.StartRequest{
		RunID:          record.ID,
		ConversationID: record.ConversationID,
		Input:          record.Input,
	})
	if err != nil {
		s.writeBackendError(w, r, "bind_tool_run_failed", err)
		return
	}
	if fingerprint != record.Handle.Fingerprint {
		writeError(w, http.StatusConflict, "run_capability_changed", "current tool capabilities no longer match the run binding")
		return
	}

	tool, ok := boundTool(bound.Tools, toolName)
	if !ok {
		writeError(w, http.StatusNotFound, "tool_not_bound", "tool is not bound to this run")
		return
	}
	if tool.Protocol != runtimesdk.ToolProtocolMCP {
		writeError(w, http.StatusNotImplemented, "tool_protocol_unsupported", "A5 tool invocation supports MCP tools only")
		return
	}
	if !tool.ReadOnly {
		writeError(w, http.StatusForbidden, "tool_not_read_only", "A5 permits only server-admitted read-only tools")
		return
	}

	var request toolInvokeRequest
	if err := s.decode(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if request.Arguments == nil {
		request.Arguments = map[string]any{}
	}
	request.InvocationID = strings.TrimSpace(request.InvocationID)
	if s.durableInvoker != nil {
		if request.InvocationID == "" {
			writeError(w, http.StatusBadRequest, "invocation_id_required", "durable tool invocation requires invocation_id")
			return
		}
		result, err := s.durableInvoker.Execute(
			r.Context(),
			record.ID,
			record.ConversationID,
			request.InvocationID,
			tool,
			request.Arguments,
		)
		if err != nil {
			if errors.Is(err, mcptransport.ErrSnapshotMismatch) ||
				errors.Is(err, mcptransport.ErrSnapshotStale) ||
				errors.Is(err, mcptransport.ErrSnapshotUnknown) {
				writeError(w, http.StatusConflict, "run_capability_changed", "tool capability snapshot changed before invocation")
				return
			}
			s.writeBackendError(w, r, "durable_tool_invoke_failed", err)
			return
		}
		// Durable activity/ledger evidence is produced by the Temporal worker,
		// not by the HTTP request. Avoid inventing synchronous event evidence.
		writeJSON(w, http.StatusOK, toolInvokeResponse{Result: result, EventPersisted: false})
		return
	}

	argumentsDigest, err := digestJSON(request.Arguments)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_arguments", "tool arguments cannot be represented as JSON")
		return
	}

	calledPayload, _ := json.Marshal(toolCallEvidence{
		InvocationID:    request.InvocationID,
		Tool:            tool.Name,
		SnapshotDigest:  tool.SnapshotDigest,
		ArgumentsDigest: argumentsDigest,
	})
	if _, err := s.store.AppendEvent(r.Context(), runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventToolCalled,
		RunID:          record.ID,
		ConversationID: record.ConversationID,
		OccurredAt:     s.clock().UTC(),
		Payload:        calledPayload,
	}); err != nil {
		s.writeBackendError(w, r, "tool_call_evidence_unavailable", err)
		return
	}

	result, err := s.invoker.Invoke(r.Context(), tool, request.Arguments)
	if err != nil {
		failedPayload, _ := json.Marshal(toolFailureEvidence{
			InvocationID:   request.InvocationID,
			Tool:           tool.Name,
			SnapshotDigest: tool.SnapshotDigest,
			Error:          err.Error(),
		})
		if _, eventErr := s.store.AppendEvent(r.Context(), runtimesdk.EventEnvelope{
			Type:           runtimesdk.EventToolFailed,
			RunID:          record.ID,
			ConversationID: record.ConversationID,
			OccurredAt:     s.clock().UTC(),
			Payload:        failedPayload,
		}); eventErr != nil {
			s.logger.Error("append tool.failed event", "run_id", record.ID, "tool", tool.Name, "error", eventErr)
		}
		if errors.Is(err, mcptransport.ErrSnapshotMismatch) ||
			errors.Is(err, mcptransport.ErrSnapshotStale) ||
			errors.Is(err, mcptransport.ErrSnapshotUnknown) {
			writeError(w, http.StatusConflict, "run_capability_changed", "tool capability snapshot changed before invocation")
			return
		}
		s.writeBackendError(w, r, "tool_invoke_failed", err)
		return
	}

	resultDigest, err := digestJSON(result)
	if err != nil {
		s.writeBackendError(w, r, "tool_result_digest_failed", err)
		return
	}
	returnedPayload, _ := json.Marshal(toolResultEvidence{
		InvocationID:   request.InvocationID,
		Tool:           tool.Name,
		SnapshotDigest: tool.SnapshotDigest,
		IsError:        result.IsError,
		ResultDigest:   resultDigest,
	})
	_, eventErr := s.store.AppendEvent(r.Context(), runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventToolReturned,
		RunID:          record.ID,
		ConversationID: record.ConversationID,
		OccurredAt:     s.clock().UTC(),
		Payload:        returnedPayload,
	})
	if eventErr != nil {
		s.logger.Error("append tool.returned event", "run_id", record.ID, "tool", tool.Name, "error", eventErr)
		writeJSON(w, http.StatusOK, toolInvokeResponse{Result: result, EventPersisted: false})
		return
	}
	writeJSON(w, http.StatusOK, toolInvokeResponse{Result: result, EventPersisted: true})
}

func boundTool(tools []runtimesdk.ToolDescriptor, name runtimesdk.ToolName) (runtimesdk.ToolDescriptor, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return runtimesdk.ToolDescriptor{}, false
}

func digestJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
