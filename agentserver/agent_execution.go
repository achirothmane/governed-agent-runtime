package agentserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type agentExecutionResponse struct {
	Outcome        agentloop.Outcome `json:"outcome"`
	EventPersisted bool              `json:"event_persisted"`
}

type agentOutcomeEvidence struct {
	Kind             agentloop.OutcomeKind `json:"kind"`
	Message          string                `json:"message,omitempty"`
	Steps            int                   `json:"steps"`
	ObservationCount int                   `json:"observation_count"`
}

func (s *Service) handleExecuteAgent(w http.ResponseWriter, r *http.Request) {
	if s.agentRunner == nil {
		writeError(w, http.StatusNotImplemented, "agent_execution_unavailable", "server has no agent execution loop")
		return
	}

	runID := runtimesdk.RunID(strings.TrimSpace(r.PathValue("runID")))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "invalid_run_id", "run id is required")
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
		s.writeBackendError(w, r, "bind_agent_run_failed", err)
		return
	}
	if fingerprint != record.Handle.Fingerprint {
		writeError(w, http.StatusConflict, "run_capability_changed", "current agent capabilities no longer match the run binding")
		return
	}

	outcome, err := s.agentRunner.Run(r.Context(), bound, runtime.Agent.Mission)
	if err != nil {
		s.writeBackendError(w, r, "agent_execution_failed", err)
		return
	}

	eventType := runtimesdk.EventRunCompleted
	switch outcome.Kind {
	case agentloop.OutcomeFinished:
		eventType = runtimesdk.EventRunCompleted
	case agentloop.OutcomeWaitingInput:
		eventType = runtimesdk.EventRunWaiting
	case agentloop.OutcomeFailed, agentloop.OutcomeMaxSteps:
		eventType = runtimesdk.EventRunFailed
	default:
		s.writeBackendError(w, r, "agent_outcome_invalid", errInvalidAgentOutcome(outcome.Kind))
		return
	}
	payload, _ := json.Marshal(agentOutcomeEvidence{
		Kind:             outcome.Kind,
		Message:          outcome.Message,
		Steps:            outcome.Steps,
		ObservationCount: len(outcome.Observations),
	})
	_, eventErr := s.store.AppendEvent(r.Context(), runtimesdk.EventEnvelope{
		Type:           eventType,
		RunID:          record.ID,
		ConversationID: record.ConversationID,
		OccurredAt:     s.clock().UTC(),
		Payload:        payload,
	})
	if eventErr != nil {
		s.logger.Error("append agent outcome event", "run_id", record.ID, "kind", outcome.Kind, "error", eventErr)
		writeJSON(w, http.StatusOK, agentExecutionResponse{Outcome: outcome, EventPersisted: false})
		return
	}
	writeJSON(w, http.StatusOK, agentExecutionResponse{Outcome: outcome, EventPersisted: true})
}

func errInvalidAgentOutcome(kind agentloop.OutcomeKind) error {
	return &invalidAgentOutcomeError{kind: kind}
}

type invalidAgentOutcomeError struct {
	kind agentloop.OutcomeKind
}

func (e *invalidAgentOutcomeError) Error() string {
	return "unsupported agent outcome " + string(e.kind)
}
