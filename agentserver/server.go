package agentserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

const (
	defaultMaxBodyBytes      int64                = 1 << 20
	defaultHeartbeatInterval                      = 15 * time.Second
	eventPageSize                                 = 256
	eventRunSignaled         runtimesdk.EventType = "run.signaled"
	eventRunCancelRequested  runtimesdk.EventType = "run.cancel_requested"
)

type Config struct {
	Provider          RuntimeProvider
	Invoker           ToolInvoker
	DurableInvoker    DurableToolInvoker
	Store             Store
	BearerToken       string
	Clock             func() time.Time
	MaxBodyBytes      int64
	HeartbeatInterval time.Duration
	Logger            *slog.Logger
}

type Service struct {
	provider          RuntimeProvider
	invoker           ToolInvoker
	durableInvoker    DurableToolInvoker
	store             Store
	bearerToken       string
	clock             func() time.Time
	maxBodyBytes      int64
	heartbeatInterval time.Duration
	logger            *slog.Logger
	mux               *http.ServeMux
}

func New(config Config) (*Service, error) {
	if config.Provider == nil {
		return nil, errors.New("agent server runtime provider is required")
	}
	if config.Store == nil {
		return nil, errors.New("agent server store is required")
	}
	if strings.TrimSpace(config.BearerToken) == "" {
		return nil, errors.New("agent server bearer token is required")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = defaultMaxBodyBytes
	}
	if config.MaxBodyBytes < 0 {
		return nil, errors.New("agent server max body bytes must be positive")
	}
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = defaultHeartbeatInterval
	}
	if config.HeartbeatInterval < 0 {
		return nil, errors.New("agent server heartbeat interval must be positive")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	invoker := config.Invoker
	if invoker == nil {
		invoker, _ = config.Provider.(ToolInvoker)
	}

	s := &Service{
		provider:          config.Provider,
		invoker:           invoker,
		durableInvoker:    config.DurableInvoker,
		store:             config.Store,
		bearerToken:       config.BearerToken,
		clock:             config.Clock,
		maxBodyBytes:      config.MaxBodyBytes,
		heartbeatInterval: config.HeartbeatInterval,
		logger:            config.Logger,
		mux:               http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

func (s *Service) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /v1/agents/{agentID}", s.handleGetAgent)
	s.mux.HandleFunc("POST /v1/conversations", s.handleCreateConversation)
	s.mux.HandleFunc("GET /v1/conversations/{conversationID}", s.handleGetConversation)
	s.mux.HandleFunc("POST /v1/conversations/{conversationID}/runs", s.handleStartRun)
	s.mux.HandleFunc("GET /v1/conversations/{conversationID}/events", s.handleEvents)
	s.mux.HandleFunc("GET /v1/runs/{runID}", s.handleGetRun)
	s.mux.HandleFunc("POST /v1/runs/{runID}/tools/{tool}", s.handleInvokeTool)
	s.mux.HandleFunc("POST /v1/runs/{runID}/signals/{signal}", s.handleSignalRun)
	s.mux.HandleFunc("POST /v1/runs/{runID}/cancel", s.handleCancelRun)
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" && !s.authorized(r) {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusUnauthorized, "unauthorized", "bearer authentication required")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Service) authorized(r *http.Request) bool {
	expected := "Bearer " + s.bearerToken
	actual := r.Header.Get("Authorization")
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := runtimesdk.AgentID(strings.TrimSpace(r.PathValue("agentID")))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_agent_id", "agent id is required")
		return
	}
	agent, err := s.provider.Agent(r.Context(), id)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

type createConversationRequest struct {
	ID      runtimesdk.ConversationID `json:"id"`
	AgentID runtimesdk.AgentID        `json:"agent_id"`
}

func (s *Service) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	var request createConversationRequest
	if err := s.decode(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(string(request.ID)) == "" || strings.TrimSpace(string(request.AgentID)) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "conversation id and agent id are required")
		return
	}
	agent, err := s.provider.Agent(r.Context(), request.AgentID)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	conversation := Conversation{
		Ref: runtimesdk.ConversationRef{
			ID:          request.ID,
			AgentID:     agent.ID,
			WorkspaceID: agent.Workspace.ID,
		},
		CreatedAt: s.clock().UTC(),
	}
	stored, created, err := s.store.PutConversation(r.Context(), conversation)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, stored)
}

func (s *Service) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	conversation, err := s.store.GetConversation(r.Context(), runtimesdk.ConversationID(r.PathValue("conversationID")))
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation)
}

type startRunRequest struct {
	ID    runtimesdk.RunID `json:"id"`
	Input string           `json:"input"`
}

type runResponse struct {
	Run            RunRecord `json:"run"`
	EventPersisted bool      `json:"event_persisted"`
}

func (s *Service) handleStartRun(w http.ResponseWriter, r *http.Request) {
	conversationID := runtimesdk.ConversationID(r.PathValue("conversationID"))
	conversation, err := s.store.GetConversation(r.Context(), conversationID)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	var request startRunRequest
	if err := s.decode(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(string(request.ID)) == "" || strings.TrimSpace(request.Input) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "run id and input are required")
		return
	}

	runtime, err := s.runtimeForConversation(r.Context(), conversation)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}

	existing, err := s.store.GetRun(r.Context(), request.ID)
	switch {
	case err == nil:
		if existing.ConversationID != conversation.Ref.ID || existing.AgentID != conversation.Ref.AgentID || existing.InputDigest != InputDigest(request.Input) {
			writeError(w, http.StatusConflict, "run_binding_conflict", "run id is already bound to a different request")
			return
		}
		handle, inspectErr := runtime.Inspect(r.Context(), request.ID)
		if inspectErr != nil {
			s.writeBackendError(w, r, "inspect_run_failed", inspectErr)
			return
		}
		if handle.Fingerprint != existing.Handle.Fingerprint {
			s.writeBackendError(w, r, "run_binding_mismatch", errors.New("backend fingerprint differs from server record"))
			return
		}
		existing.Handle = handle
		// This retry did not persist a new event. A prior run.started event may
		// exist, but the response never claims evidence it did not write itself.
		writeJSON(w, http.StatusOK, runResponse{Run: existing, EventPersisted: false})
		return
	case !errors.Is(err, ErrRunNotFound):
		s.writeMappedError(w, r, err)
		return
	}

	handle, err := runtime.Start(r.Context(), runtimesdk.StartRequest{
		RunID:          request.ID,
		ConversationID: conversation.Ref.ID,
		Input:          request.Input,
	})
	if err != nil {
		s.writeBackendError(w, r, "start_run_failed", err)
		return
	}
	record, err := NewRunRecord(conversation, request.Input, handle, s.clock().UTC())
	if err != nil {
		s.writeBackendError(w, r, "run_record_invalid", err)
		return
	}
	stored, created, err := s.store.PutRun(r.Context(), record)
	if err != nil {
		// The durable backend may already have started the run. Returning 503 is
		// intentional: retrying the same request is safe because A2 reconciles
		// duplicate starts by the exact run fingerprint.
		s.writeBackendError(w, r, "run_metadata_unavailable", err)
		return
	}
	if stored.Handle.Fingerprint != handle.Fingerprint {
		s.writeBackendError(w, r, "run_binding_mismatch", errors.New("stored run fingerprint differs from started run"))
		return
	}
	if !created {
		// Another request won the metadata race. Do not append a duplicate
		// run.started event; the durable backend identity has already reconciled.
		writeJSON(w, http.StatusOK, runResponse{Run: stored, EventPersisted: false})
		return
	}

	payload, _ := json.Marshal(handle)
	_, eventErr := s.store.AppendEvent(r.Context(), runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventRunStarted,
		RunID:          handle.ID,
		ConversationID: conversation.Ref.ID,
		OccurredAt:     s.clock().UTC(),
		Payload:        payload,
	})
	if eventErr != nil {
		s.logger.Error("append run.started event", "run_id", handle.ID, "error", eventErr)
		writeJSON(w, http.StatusAccepted, runResponse{Run: stored, EventPersisted: false})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, runResponse{Run: stored, EventPersisted: true})
}

func (s *Service) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := runtimesdk.RunID(r.PathValue("runID"))
	record, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	runtime, err := s.runtimeForRun(r.Context(), record)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	handle, err := runtime.Inspect(r.Context(), id)
	if err != nil {
		s.writeBackendError(w, r, "inspect_run_failed", err)
		return
	}
	if handle.Fingerprint != record.Handle.Fingerprint {
		s.writeBackendError(w, r, "run_binding_mismatch", errors.New("backend fingerprint differs from server record"))
		return
	}
	record.Handle = handle
	writeJSON(w, http.StatusOK, record)
}

type signalRequest struct {
	Payload json.RawMessage `json:"payload"`
}

type operationResponse struct {
	Accepted       bool `json:"accepted"`
	EventPersisted bool `json:"event_persisted"`
}

func (s *Service) handleSignalRun(w http.ResponseWriter, r *http.Request) {
	id := runtimesdk.RunID(r.PathValue("runID"))
	signal := strings.TrimSpace(r.PathValue("signal"))
	if signal == "" {
		writeError(w, http.StatusBadRequest, "invalid_signal", "signal name is required")
		return
	}
	record, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	var request signalRequest
	if err := s.decode(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(request.Payload) == 0 {
		request.Payload = json.RawMessage("null")
	}
	if !json.Valid(request.Payload) {
		writeError(w, http.StatusBadRequest, "invalid_request", "signal payload must be valid JSON")
		return
	}
	runtime, err := s.runtimeForRun(r.Context(), record)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	if err := runtime.Signal(r.Context(), id, signal, request.Payload); err != nil {
		s.writeBackendError(w, r, "signal_run_failed", err)
		return
	}
	payload, _ := json.Marshal(struct {
		Name    string          `json:"name"`
		Payload json.RawMessage `json:"payload"`
	}{Name: signal, Payload: request.Payload})
	_, eventErr := s.store.AppendEvent(r.Context(), runtimesdk.EventEnvelope{
		Type:           eventRunSignaled,
		RunID:          id,
		ConversationID: record.ConversationID,
		OccurredAt:     s.clock().UTC(),
		Payload:        payload,
	})
	if eventErr != nil {
		s.logger.Error("append run.signaled event", "run_id", id, "error", eventErr)
		writeJSON(w, http.StatusAccepted, operationResponse{Accepted: true, EventPersisted: false})
		return
	}
	writeJSON(w, http.StatusAccepted, operationResponse{Accepted: true, EventPersisted: true})
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

func (s *Service) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := runtimesdk.RunID(r.PathValue("runID"))
	record, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	var request cancelRequest
	if err := s.decode(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(request.Reason) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "cancellation reason is required")
		return
	}
	runtime, err := s.runtimeForRun(r.Context(), record)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	if err := runtime.Cancel(r.Context(), id, request.Reason); err != nil {
		s.writeBackendError(w, r, "cancel_run_failed", err)
		return
	}
	payload, _ := json.Marshal(request)
	_, eventErr := s.store.AppendEvent(r.Context(), runtimesdk.EventEnvelope{
		Type:           eventRunCancelRequested,
		RunID:          id,
		ConversationID: record.ConversationID,
		OccurredAt:     s.clock().UTC(),
		Payload:        payload,
	})
	if eventErr != nil {
		s.logger.Error("append run.cancel_requested event", "run_id", id, "error", eventErr)
		writeJSON(w, http.StatusAccepted, operationResponse{Accepted: true, EventPersisted: false})
		return
	}
	writeJSON(w, http.StatusAccepted, operationResponse{Accepted: true, EventPersisted: true})
}

func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	conversationID := runtimesdk.ConversationID(r.PathValue("conversationID"))
	if _, err := s.store.GetConversation(r.Context(), conversationID); err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	after, err := eventCursor(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_event_cursor", err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support streaming")
		return
	}
	notifications, cancelWatch, err := s.store.Watch(r.Context(), conversationID)
	if err != nil {
		s.writeMappedError(w, r, err)
		return
	}
	defer cancelWatch()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(s.heartbeatInterval)
	defer heartbeat.Stop()

	for {
		drained := false
		for !drained {
			events, err := s.store.ListEvents(r.Context(), conversationID, after, eventPageSize)
			if err != nil {
				s.logger.Error("list stream events", "conversation_id", conversationID, "error", err)
				return
			}
			for _, event := range events {
				if err := writeSSE(w, event); err != nil {
					return
				}
				after = event.Sequence
			}
			if len(events) < eventPageSize {
				drained = true
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}

		select {
		case <-r.Context().Done():
			return
		case <-notifications:
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Service) runtimeForRun(ctx context.Context, record RunRecord) (runtimesdk.Runtime, error) {
	conversation, err := s.store.GetConversation(ctx, record.ConversationID)
	if err != nil {
		return runtimesdk.Runtime{}, err
	}
	if conversation.Ref.AgentID != record.AgentID {
		return runtimesdk.Runtime{}, fmt.Errorf("%w: run agent does not match conversation binding", ErrConflict)
	}
	return s.runtimeForConversation(ctx, conversation)
}

func (s *Service) runtimeForConversation(ctx context.Context, conversation Conversation) (runtimesdk.Runtime, error) {
	runtime, err := s.provider.Runtime(ctx, conversation.Ref.AgentID)
	if err != nil {
		return runtimesdk.Runtime{}, err
	}
	if runtime.Agent.ID != conversation.Ref.AgentID || runtime.Agent.Workspace.ID != conversation.Ref.WorkspaceID {
		return runtimesdk.Runtime{}, fmt.Errorf("%w: runtime no longer matches conversation binding", ErrConflict)
	}
	return runtime, nil
}

func (s *Service) decode(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON value")
	}
	return nil
}

func (s *Service) writeMappedError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "agent not found")
	case errors.Is(err, ErrConversationNotFound):
		writeError(w, http.StatusNotFound, "conversation_not_found", "conversation not found")
	case errors.Is(err, ErrRunNotFound):
		writeError(w, http.StatusNotFound, "run_not_found", "run not found")
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, "binding_conflict", err.Error())
	default:
		s.writeBackendError(w, r, "server_error", err)
	}
}

func (s *Service) writeBackendError(w http.ResponseWriter, r *http.Request, code string, err error) {
	s.logger.Error("agent server request failed", "method", r.Method, "path", r.URL.Path, "code", code, "error", err)
	writeError(w, http.StatusServiceUnavailable, code, "runtime operation unavailable; retry with the same bound request")
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func eventCursor(r *http.Request) (uint64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("after"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if raw == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("event cursor must be an unsigned integer")
	}
	return cursor, nil
}

func writeSSE(w io.Writer, event runtimesdk.EventEnvelope) error {
	if strings.ContainsAny(string(event.Type), "\r\n") {
		return errors.New("event type contains an invalid line break")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data); err != nil {
		return err
	}
	return nil
}
