package governance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

type AdmissionRequest struct {
	Subject Subject
	Plan    []byte
}

// Grant is returned by host-owned policy, never supplied by the requester.
type Grant struct {
	Target     string
	Profile    string
	Witness    Witness
	ValidUntil time.Time
}

type ServiceConfig struct {
	PrivateKey    ed25519.PrivateKey
	BearerToken   string
	Clock         func() time.Time
	Authorize     func(context.Context, AdmissionRequest) (Grant, error)
	Revalidate    func(context.Context, Admission) (Witness, error)
	MaxAdmissions int
}

// Service is a fail-closed admission issuer; it does not execute external tools.
// Its bounded issuance ledger is volatile: restart invalidates outstanding IDs.
type Service struct {
	config ServiceConfig
	mu     sync.RWMutex
	issued map[string]Admission
}

func NewService(config ServiceConfig) (*Service, error) {
	if len(config.PrivateKey) != ed25519.PrivateKeySize || config.BearerToken == "" ||
		config.Clock == nil || config.Authorize == nil || config.Revalidate == nil || config.MaxAdmissions <= 0 {
		return nil, ErrAdmission
	}
	config.PrivateKey = append(ed25519.PrivateKey(nil), config.PrivateKey...)
	return &Service{config: config, issued: make(map[string]Admission)}, nil
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	expected := "Bearer " + s.config.BearerToken
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	switch r.URL.Path {
	case "/v1/admissions":
		var request AdmissionRequest
		if decodeStrict(r.Body, &request) != nil || validateRequest(request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		grant, err := s.config.Authorize(r.Context(), request)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		a := Admission{
			ID: hex.EncodeToString(random[:]), AgentID: request.Subject.AgentID,
			EventID: request.Subject.EventID, WorkerID: request.Subject.WorkerID,
			LeaseEpoch: request.Subject.LeaseEpoch, PlanDigest: request.Subject.PlanDigest,
			Target: grant.Target, Profile: grant.Profile, Witness: grant.Witness,
			IssuedAt: s.config.Clock().UTC(), ValidUntil: grant.ValidUntil.UTC(),
		}
		document, err := json.Marshal(a)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		signed := SignedAdmission{Document: document, Signature: ed25519.Sign(s.config.PrivateKey, document)}
		verifier := Verifier{
			PublicKey: s.config.PrivateKey.Public().(ed25519.PublicKey),
			Clock:     s.config.Clock, Current: s.config.Revalidate,
		}
		if _, err := verifier.Verify(r.Context(), signed, request.Subject, s.config.Clock()); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		// Normalize the ledger to the exact wire document (no monotonic time bits).
		if json.Unmarshal(document, &a) != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s.mu.Lock()
		if len(s.issued) >= s.config.MaxAdmissions {
			s.mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s.issued[a.ID] = a
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(signed)
	case "/v1/admissions/current":
		var a Admission
		if decodeStrict(r.Body, &a) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.RLock()
		issued, ok := s.issued[a.ID]
		s.mu.RUnlock()
		if !ok || issued != a {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		witness, err := s.config.Revalidate(r.Context(), issued)
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(witness)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func validateRequest(request AdmissionRequest) error {
	subject := request.Subject
	if subject.AgentID == "" || subject.EventID == "" || subject.WorkerID == "" ||
		subject.LeaseEpoch == 0 || len(request.Plan) == 0 || !json.Valid(request.Plan) {
		return ErrAdmission
	}
	sum := sha256.Sum256(request.Plan)
	if hex.EncodeToString(sum[:]) != subject.PlanDigest {
		return ErrAdmission
	}
	var plan struct {
		AgentID string `json:"agent_id"`
		EventID string `json:"event_id"`
	}
	if json.Unmarshal(request.Plan, &plan) != nil || plan.AgentID != subject.AgentID || plan.EventID != subject.EventID {
		return ErrAdmission
	}
	return nil
}

func decodeStrict(reader io.Reader, target any) error {
	d := json.NewDecoder(reader)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrAdmission
	}
	return nil
}
