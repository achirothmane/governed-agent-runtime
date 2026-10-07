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
	Store             Store
	BearerToken       string
	Clock             func() time.Time
	MaxBodyBytes      int64
	HeartbeatInterval time.Duration
	Logger            *slog.Logger
}

type Service struct {
	provider          RuntimeProvider
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

	s := &Service{
		provider:          config.Provider,
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
		writeJSON(w, http.StatusOK, runResponse{Run: existing, EventPersisted: true})
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
	writeJSON(w, status, runResponse{Run: stored, EventPersisted: trueY_JBŸB‚™[˜È
È
”Ù\šXÙJH[™QÙ][ŠÈ”™\ÜÛœÙUÜš]\‹ˆ
š”™\]Y\Ý
HÂ‚ZYH[[Y\ÙË”[’Q
‹”]˜[YJœ[’QŠJB‚\™XÛÜ™\œˆHËœÝÜ™K‘Ù][Š‹ÛÛ^

KY
B‚ZYˆ\œˆOHš[Â‚B\ËÜš]SX\Y\œ›ÜŠË‹\œŠB‚B\™]\›‚‚_B‚\[[YK\œˆHËœ›ÝšY\‹”[[YJ‹ÛÛ^

K™XÛÜ™YÙ[Q
B‚ZYˆ\œˆOHš[Â‚B\ËÜš]SX\Y\œ›ÜŠË‹\œŠB‚B\™]\›‚‚_B‚Z[™K\œˆH[[YK’[œÜXÝ
‹ÛÛ^

KY
B‚ZYˆ\œˆOHš[Â‚B\ËÜš]P˜XÚÙ[™\œ›ÜŠË‹š[œÜXÝÜ[—Ù˜Z[Y‹\œŠB‚B\™]\›‚‚_B‚ZYˆ[™K‘š[™Ù\œš[OH™XÛÜ™’[™K‘š[™Ù\œš[Â‚B\ËÜš]P˜XÚÙ[™\œ›ÜŠË‹œ[—Øš[™[™×ÛZ\ÛX]Ú‹\œ›ÜœË“™]Ê˜˜XÚÙ[™š[™Ù\œš[Y™™\œÈœ›ÛHÙ\™\ˆ™XÛÜ™ŠJB‚B\™]\›‚‚_B‚\™XÛÜ™’[™HH[™B‚]Üš]R”ÓÓŠË”Ý]\ÓÒË™XÛÜ™
BŸB‚\HÚYÛ˜[™\]Y\ÝÝXÝÂ‚T^[ØYœÛÛ‹”˜]ÓY\ÜØYÙHœÛÛŽˆœ^[ØY˜ŸB‚\HÜ\˜][Û”™\ÜÛœÙHÝXÝÂ‚PXØÙ\Y›ÛÛœÛÛŽˆ˜XØÙ\Y˜‚Q]™[\œÚ\ÝY›ÛÛœÛÛŽˆ™]™[Ü\œÚ\ÝY˜ŸB‚™[˜È
È
”Ù\šXÙJH[™TÚYÛ˜[[ŠÈ”™\ÜÛœÙUÜš]\‹ˆ
š”™\]Y\Ý
HÂ‚ZYH[[Y\ÙË”[’Q
‹”]˜[YJœ[’QŠJB‚\ÚYÛ˜[HÝš[™ÜË•š[TÜXÙJ‹”]˜[YJœÚYÛ˜[ŠJB‚ZYˆÚYÛ˜[OHˆˆÂ‚B]Üš]Q\œ›ÜŠË”Ý]\Ð˜Y™\]Y\Ýš[˜[YÜÚYÛ˜[‹œÚYÛ˜[˜[YH\È™\]Z\™YŠB‚B\™]\›‚‚_B‚\™XÛÜ™\œˆHËœÝÜ™K‘Ù][Š‹ÛÛ^

KY
B‚ZYˆ\œˆOHš[Â‚B\ËÜš]SX\Y\œ›ÜŠË‹\œŠB‚B\™]\›‚‚_B‚]˜\ˆ™\]Y\ÝÚYÛ˜[™\]Y\Ý‚ZYˆ\œˆHË™XÛÙJË‹	œ™\]Y\Ý
NÈ\œˆOHš[Â‚B]Üš]Q\œ›ÜŠË”Ý]\Ð˜Y™\]Y\Ýš[˜[YÜ™\]Y\Ý‹\œ‹‘\œ›ÜŠ
JB‚B\™]\›‚‚_B‚ZYˆ[Š™\]Y\Ý”^[ØY
HOHÂ‚B\™\]Y\Ý”^[ØYHœÛÛ‹”˜]ÓY\ÜØYÙJ›[ŠB‚_B‚ZYˆZœÛÛ‹•˜[Y
™\]Y\Ý”^[ØY
HÂ‚B]Üš]Q\œ›ÜŠË”Ý]\Ð˜Y™\]Y\Ýš[˜[YÜ™\]Y\Ý‹œÚYÛ˜[^[ØY]\Ý™H˜[Y”ÓÓˆŠB‚B\™]\›‚‚_B‚\[[YK\œˆHËœ›ÝšY\‹”[[YJ‹ÛÛ^

K™XÛÜ™YÙ[Q
B‚ZYˆ\œˆOHš[Â‚B\ËÜš]SX\Y\œ›ÜŠË‹\œŠB‚B\™]\›‚‚_B‚ZYˆ\œˆH[[YK”ÚYÛ˜[
‹ÛÛ^

KYÚYÛ˜[™\]Y\Ý”^[ØY
NÈ\œˆOHš[Â‚B\ËÜš]P˜XÚÙ[™\œ›ÜŠË‹œÚYÛ˜[Ü[—Ù˜Z[Y‹\œŠB‚B\™]\›‚‚_B‚\^[ØYÈHœÛÛ‹“X\œÚ[
ÝXÝÂ‚BS˜[YHÝš[™ÈœÛÛŽˆ›˜[YH˜‚BT^[ØYœÛÛ‹”˜]ÓY\ÜØYÙHœÛÛŽˆœ^[ØY˜‚_^Ó˜[YNˆÚYÛ˜[^[ØYˆ™\]Y\Ý”^[ØYJB‚WË]™[\œˆHËœÝÜ™K\[™]™[
‹ÛÛ^

K[[Y\ÙË‘]™[[™[Ü^Â‚BU\Nˆ]™[[”ÚYÛ˜[Y‚BT[’QˆY‚BPÛÛ™\œØ][Û’Qˆ™XÛÜ™ÛÛ™\œØ][Û’Q‚BSØØÝ\œ™Y]ˆË˜ÛØÚÊ
K•UÊ
K‚BT^[ØYˆ^[ØY‚_JB‚ZYˆ]™[\œˆOHš[Â‚B\Ë›ÙÙÙ\‹‘\œ›ÜŠ˜\[™[‹œÚYÛ˜[Y]™[‹œ[—ÚY‹Y™\œ›Üˆ‹]™[\œŠB‚B]Üš]R”ÓÓŠË”Ý]\ÐXØÙ\YÜ\˜][Û”™\ÜÛœÙ^ÐXØÙ\YˆYK]™[\œÚ\ÝYˆ˜[Ù_JB‚B\™]\›‚‚_B‚]Üš]R”ÓÓŠË”Ý]\ÐXØÙ\YÜ\˜][Û”™\ÜÛœÙ^ÐXØÙ\YˆYK]™[\œÚ\ÝYˆY_JBŸB‚\HØ[˜Ù[™\]Y\ÝÝXÝÂ‚T™X\ÛÛˆÝš[™ÈœÛÛŽˆœ™X\ÛÛˆ˜ŸB‚™[˜È
È
”Ù\šXÙJH[™PØ[˜Ù[[ŠÈ”™\ÜÛœÙUÜš]\‹ˆ
š”™\]Y\Ý
HÂ‚ZYH[[Y\ÙË”[’Q
‹”]˜[YJœ[’QŠJB‚\™XÛÜ™\œˆHËœÝÜ™K‘Ù][Š‹ÛÛ^

KY
B‚ZYˆ\œˆOHš[Â‚B\ËÜš]SX\Y\œ›ÜŠË‹\œŠB‚B\™]\›‚‚_B‚]˜\ˆ™\]Y\ÝØ[˜Ù[™\]Y\Ý‚ZYˆ\œˆHË™XÛÙJË‹	œ™\]Y\Ý
NÈ\œˆOHš[Â‚B]Üš]Q\œ›ÜŠË”Ý]\Ð˜Y™\]Y\Ýš[˜[YÜ™\]Y\Ý‹\œ‹‘\œ›ÜŠ
JB‚B\™]\›‚‚_B‚ZYˆÝš[™ÜË•š[TÜXÙJ™\]Y\Ý”™X\ÛÛŠHOHˆˆÂ‚B]Üš]Q\œ›ÜŠË”Ý]\Ð˜Y™\]Y\Ýš[˜[YÜ™\]Y\Ý‹˜Ø[˜Ù[][Ûˆ™X\ÛÛˆ\È™\]Z\™YŠB‚B\™]\›‚‚_B‚\[[YK\œˆHËœ›ÝšY\‹”[[YJ‹ÛÛ^

K™XÛÜ™YÙ[Q
B‚ZYˆ\œˆOHš[Â‚B\ËÜš]SX\Y\œ›ÜŠË‹\œŠB‚B\™]\›‚‚_B‚ZYˆ\œˆH[[YKØ[˜Ù[
‹ÛÛ^

KY™\]Y\Ý”™X\ÛÛŠNÈ\œˆOHš[Â‚B\ËÜš]P˜XÚÙ[™\œ›ÜŠË‹˜Ø[˜Ù[Ü[—Ù˜Z[Y‹\œŠB‚B\™]\›‚‚_B‚\^[ØYÈHœÛÛ‹“X\œÚ[
™\]Y\Ý
B‚WË]™[\œˆHËœÝÜ™K\[™]™[
‹ÛÛ^

K[[Y\ÙË‘]™[[™[Ü^Â‚BU\Nˆ]™[[Ø[˜Ù[™\]Y\ÝY‚BT[’QˆY‚BPÛÛ™\œØ][Û’Qˆ™XÛÜ™ÛÛ™\œØ][Û’Q‚BSØØÝ\œ™Y]ˆË˜ÛØÚÊ
K•UÊ
K‚BT^[ØYˆ^[ØY‚_JB‚ZYˆ]™[\œˆOHš[Â‚B\Ë›ÙÙÙ\‹‘\œ›ÜŠ˜\[™[‹˜Ø[˜Ù[Ü™\]Y\ÝY]™[‹œ[—ÚY‹Y™\œ›Üˆ‹]™[\œŠB‚B]Üš]R”ÓÓŠË”Ý]\ÐXØÙ\YÜ\˜][Û”™\ÜÛœÙ^ÐXØÙ\YˆYK]™[\œÚ\ÝYˆ˜[Ù_JB‚B\™]\›‚‚_B‚]Üš]R”ÓÓŠË”Ý]\ÐXØÙ\YÜ\˜][Û”™\ÜÛœÙ^ÐXØÙ\YˆYK]™[\œÚ\ÝYˆY_JBŸB‚™[˜È
È
”Ù\šXÙJH[™Q]™[ÊÈ”™\ÜÛœÙUÜš]\‹ˆ
š”™\]Y\Ý
HÂ‚XÛÛ™\œØ][Û’QH[[Y\ÙËÛÛ™\œØ][Û’Q
‹”]˜[YJ˜ÛÛ™\œØ][Û’QŠJB‚ZYˆË\œˆHËœÝÜ™K‘Ù]ÛÛ™\œØ][ÛŠ‹ÛÛ^

KÛÛ™\œØ][Û’Q
NÈ\œˆOHš[Â‚B\ËÜš]SX\Y\œ›ÜŠË‹\œŠB‚B\™]\›‚‚_B‚XY\‹\œˆH]™[Ý\œÛÜŠŠB‚ZYˆ\œˆOHš[Â‚B]Üš]Q\œ›ÜŠË”Ý]\Ð˜Y™\]Y\Ýš[˜[YÙ]™[ØÝ\œÛÜˆ‹\œ‹‘\œ›ÜŠ
JB‚B\™]\›‚‚_B‚Y›\Ú\‹ÚÈHËŠ‘›\Ú\ŠB‚ZYˆ[ÚÈÂ‚B]Üš]Q\œ›ÜŠË”Ý]\Ò[\›˜[Ù\™\‘\œ›Ü‹œÝ™X[Z[™×Ý[œÝ\ÜY‹œ™\ÜÛœÙHÜš]\ˆÙ\È›ÝÝ\ÜÝ™X[Z[™ÈŠB‚B\™]\›‚‚_B‚[›ÝYšXØ][ÛœËØ[˜Ù[Ø]Ú\œˆHËœÝÜ™K•Ø]Ú
‹ÛÛ^

KÛÛ™\œØ][Û’Q
B‚ZYˆ\œˆOHš[Â‚B\ËÜš]SX\Y\œ›ÜŠË‹\œŠB‚B\™]\›‚‚_B‚YY™\ˆØ[˜Ù[Ø]Ú

B‚‚]Ë’XY\Š
K”Ù]
ÛÛ[U\H‹^Ù]™[\Ý™X[HŠB‚]Ë’XY\Š
K”Ù]
ØXÚKPÛÛ›Û‹››Ë\ÝÜ™HŠB‚]Ë’XY\Š
K”Ù]
–PXØÙ[PY™™\š[™È‹››ÈŠB‚]Ë•Üš]RXY\Š”Ý]\ÓÒÊB‚Y›\Ú\‹‘›\Ú

B‚‚ZX\™X]H[YK“™]ÕXÚÙ\ŠËšX\™X][\˜[
B‚YY™\ˆX\™X]”ÝÜ

B‚‚Y›ÜˆÂ‚BY˜Z[™YH˜[ÙB‚BY›ÜˆY˜Z[™YÂ‚BBY]™[Ë\œˆHËœÝÜ™K“\Ý]™[Ê‹ÛÛ^

KÛÛ™\œØ][Û’QY\‹]™[YÙTÚ^™JB‚BBZYˆ\œˆOHš[Â‚BBB\Ë›ÙÙÙ\‹‘\œ›ÜŠ›\ÝÝ™X[H]™[È‹˜ÛÛ™\œØ][Û—ÚY‹ÛÛ™\œØ][Û’Q™\œ›Üˆ‹\œŠB‚BBB\™]\›‚‚BB_B‚BBY›ÜˆË]™[H˜[™ÙH]™[ÈÂ‚BBBZYˆ\œˆHÜš]TÔÑJË]™[
NÈ\œˆOHš[Â‚BBBB\™]\›‚‚BBB_B‚BBBXY\ˆH]™[”Ù\]Y[˜ÙB‚BB_B‚BBZYˆ[Š]™[ÊH]™[YÙTÚ^™HÂ‚BBBY˜Z[™YHYB‚BB_B‚BBZYˆ[Š]™[ÊHˆÂ‚BBBY›\Ú\‹‘›\Ú

B‚BB_B‚B_B‚‚B\Ù[XÝÂ‚BXØ\ÙH\‹ÛÛ^

K‘Û™J
N‚‚BB\™]\›‚‚BXØ\ÙH[›ÝYšXØ][ÛœÎ‚‚BXØ\ÙHZX\™X]Î‚‚BBZYˆË\œˆH[Ë•Üš]TÝš[™ÊËŽˆÙY\[]™W—ˆŠNÈ\œˆOHš[Â‚BBB\™]\›‚‚BB_B‚BBY›\Ú\‹‘›\Ú

B‚B_B‚_BŸB‚™[˜È
È
”Ù\šXÙJH[[YQ›ÜÛÛ™\œØ][ÛŠÝÛÛ^ÛÛ^ÛÛ™\œØ][ÛˆÛÛ™\œØ][ÛŠH
[[Y\ÙË”[[YK\œ›ÜŠHÂ‚\[[YK\œˆHËœ›ÝšY\‹”[[YJÝÛÛ™\œØ][Û‹”™Y‹YÙ[Q
B‚ZYˆ\œˆOHš[Â‚B\™]\›ˆ[[Y\ÙË”[[Y^ßK\œ‚‚_B‚ZYˆ[[YKYÙ[’QOHÛÛ™\œØ][Û‹”™Y‹YÙ[Q[[YKYÙ[•ÛÜšÜÜXÙK’QOHÛÛ™\œØ][Û‹”™Y‹•ÛÜšÜÜXÙRQÂ‚B\™]\›ˆ[[Y\ÙË”[[Y^ßK›]‘\œ›Ü™Š‰]Îˆ[[YH›ÈÛ™Ù\ˆX]Ú\ÈÛÛ™\œØ][Ûˆš[™[™È‹\œÛÛ™›XÝ
B‚_B‚\™]\›ˆ[[YKš[ŸB‚™[˜È
È
”Ù\šXÙJHXÛÙJÈ”™\ÜÛœÙUÜš]\‹ˆ
š”™\]Y\Ý\™Ù][žJH\œ›ÜˆÂ‚\‹›ÙHH“X^ž]\Ô™XY\ŠË‹›ÙKË›X^›ÙPž]\ÊB‚YXÛÙ\ˆHœÛÛ‹“™]ÑXÛÙ\Š‹›ÙJB‚YXÛÙ\‹‘\Ø[ÝÕ[šÛ›ÝÛ‘šY[Ê
B‚ZYˆ\œˆHXÛÙ\‹‘XÛÙJ\™Ù]
NÈ\œˆOHš[Â‚B\™]\›ˆ\œ‚‚_B‚]˜\ˆ^˜H[žB‚ZYˆ\œˆHXÛÙ\‹‘XÛÙJ	™^˜JNÈY\œ›ÜœË’\Ê\œ‹[Ë‘SÑŠHÂ‚B\™]\›ˆ\œ›ÜœË“™]Êœ™\]Y\Ý›ÙH]\ÝÛÛZ[ˆ^XÝHÛ™H”ÓÓˆ˜[YHŠB‚_B‚\™]\›ˆš[ŸB‚™[˜È
È
”Ù\šXÙJHÜš]SX\Y\œ›ÜŠÈ”™\ÜÛœÙUÜš]\‹ˆ
š”™\]Y\Ý\œˆ\œ›ÜŠHÂ‚\ÝÚ]ÚÂ‚XØ\ÙH\œ›ÜœË’\Ê\œ‹\œYÙ[›Ý›Ý[™
N‚‚B]Üš]Q\œ›ÜŠË”Ý]\Ó›Ý›Ý[™˜YÙ[Û›ÝÙ›Ý[™‹˜YÙ[›Ý›Ý[™ŠB‚XØ\ÙH\œ›ÜœË’\Ê\œ‹\œÛÛ™\œØ][Û“›Ý›Ý[™
N‚‚B]Üš]Q\œ›ÜŠË”Ý]\Ó›Ý›Ý[™˜ÛÛ™\œØ][Û—Û›ÝÙ›Ý[™‹˜ÛÛ™\œØ][Ûˆ›Ý›Ý[™ŠB‚XØ\ÙH\œ›ÜœË’\Ê\œ‹\œ”[“›Ý›Ý[™
N‚‚B]Üš]Q\œ›ÜŠË”Ý]\Ó›Ý›Ý[™œ[—Û›ÝÙ›Ý[™‹œ[ˆ›Ý›Ý[™ŠB‚XØ\ÙH\œ›ÜœË’\Ê\œ‹\œÛÛ™›XÝ
N‚‚B]Üš]Q\œ›ÜŠË”Ý]\ÐÛÛ™›XÝ˜š[™[™×ØÛÛ™›XÝ‹\œ‹‘\œ›ÜŠ
JB‚YY˜][‚‚B\ËÜš]P˜XÚÙ[™\œ›ÜŠË‹œÙ\™\—Ù\œ›Üˆ‹\œŠB‚_BŸB‚™[˜È
È
”Ù\šXÙJHÜš]P˜XÚÙ[™\œ›ÜŠÈ”™\ÜÛœÙUÜš]\‹ˆ
š”™\]Y\ÝÛÙHÝš[™Ë\œˆ\œ›ÜŠHÂ‚\Ë›ÙÙÙ\‹‘\œ›ÜŠ˜YÙ[Ù\™\ˆ™\]Y\Ý˜Z[Y‹›Y]Ù‹‹“Y]Ùœ]‹‹•T“”]˜ÛÙH‹ÛÙK™\œ›Üˆ‹\œŠB‚]Üš]Q\œ›ÜŠË”Ý]\ÔÙ\šXÙU[˜]˜Z[X›KÛÙKœ[[YHÜ\˜][Ûˆ[˜]˜Z[X›NÈ™]žHÚ]HØ[YH›Ý[™™\]Y\ÝŠBŸB‚\H\œ›Ü›ÙHÝXÝÂ‚Q\œ›ÜˆÝXÝÂ‚BPÛÙHÝš[™ÈœÛÛŽˆ˜ÛÙH˜‚BSY\ÜØYÙHÝš[™ÈœÛÛŽˆ›Y\ÜØYÙH˜‚_HœÛÛŽˆ™\œ›Üˆ˜ŸB‚™[˜ÈÜš]Q\œ›ÜŠÈ”™\ÜÛœÙUÜš]\‹Ý]\È[ÛÙKY\ÜØYÙHÝš[™ÊHÂ‚]˜\ˆ›ÙH\œ›Ü›ÙB‚X›ÙK‘\œ›Ü‹ÛÙHHÛÙB‚X›ÙK‘\œ›Ü‹“Y\ÜØYÙHHY\ÜØYÙB‚]Üš]R”ÓÓŠËÝ]\Ë›ÙJBŸB‚™[˜ÈÜš]R”ÓÓŠÈ”™\ÜÛœÙUÜš]\‹Ý]\È[˜[YH[žJHÂ‚]Ë’XY\Š
K”Ù]
ÛÛ[U\H‹˜\XØ][Û‹ÚœÛÛˆŠB‚]Ë’XY\Š
K”Ù]
ØXÚKPÛÛ›Û‹››Ë\ÝÜ™HŠB‚]Ë•Üš]RXY\ŠÝ]\ÊB‚WÈHœÛÛ‹“™]Ñ[˜ÛÙ\ŠÊK‘[˜ÛÙJ˜[YJBŸB‚™[˜È]™[Ý\œÛÜŠˆ
š”™\]Y\Ý
H
Z[\œ›ÜŠHÂ‚\˜]ÈHÝš[™ÜË•š[TÜXÙJ‹•T“”]Y\žJ
K‘Ù]
˜Y\ˆŠJB‚ZYˆ˜]ÈOHˆˆÂ‚B\˜]ÈHÝš[™ÜË•š[TÜXÙJ‹’XY\‹‘Ù]
“\ÝQ]™[RQŠJB‚_B‚ZYˆ˜]ÈOHˆˆÂ‚B\™]\›ˆš[‚_B‚XÝ\œÛÜ‹\œˆHÝ˜ÛÛ‹”\œÙUZ[
˜]ËL
B‚ZYˆ\œˆOHš[Â‚B\™]\›ˆ\œ›ÜœË“™]Ê™]™[Ý\œÛÜˆ]\Ý™H[ˆ[œÚYÛ™Y[YÙ\ˆŠB‚_B‚\™]\›ˆÝ\œÛÜ‹š[ŸB‚™[˜ÈÜš]TÔÑJÈ[Ë•Üš]\‹]™[[[Y\ÙË‘]™[[™[ÜJH\œ›ÜˆÂ‚Y]K\œˆHœÛÛ‹“X\œÚ[
]™[
B‚ZYˆ\œˆOHš[Â‚B\™]\›ˆ\œ‚‚_B‚ZYˆË\œˆH›]‘œš[ŠËšYˆ	Y™]™[ˆ	\×™]Nˆ	\×—ˆ‹]™[”Ù\]Y[˜ÙK]™[•\K]JNÈ\œˆOHš[Â‚B\™]\›ˆ\œ‚‚_B‚\™]\›ˆš[ŸB