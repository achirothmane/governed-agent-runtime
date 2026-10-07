package agentserver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

// MemoryStore is a reference server store for local use and tests. It provides
// concurrency-safe idempotency and replayable event streaming, but it is not a
// restart-durable database. Production composition may replace it through Store.
type MemoryStore struct {
	mu sync.RWMutex

	conversations map[runtimesdk.ConversationID]Conversation
	runs          map[runtimesdk.RunID]RunRecord
	events        map[runtimesdk.ConversationID][]runtimesdk.EventEnvelope
	watchers      map[runtimesdk.ConversationID]map[uint64]chan struct{}
	nextWatcher   uint64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		conversations: make(map[runtimesdk.ConversationID]Conversation),
		runs:          make(map[runtimesdk.RunID]RunRecord),
		events:        make(map[runtimesdk.ConversationID][]runtimesdk.EventEnvelope),
		watchers:      make(map[runtimesdk.ConversationID]map[uint64]chan struct{}),
	}
}

func (s *MemoryStore) PutConversation(_ context.Context, conversation Conversation) (Conversation, bool, error) {
	if s == nil {
		return Conversation{}, false, errors.New("memory store is nil")
	}
	if err := conversation.Validate(); err != nil {
		return Conversation{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.conversations[conversation.Ref.ID]
	if ok {
		if existing.Ref != conversation.Ref {
			return Conversation{}, false, fmt.Errorf("%w: conversation %s", ErrConflict, conversation.Ref.ID)
		}
		return existing, false, nil
	}
	s.conversations[conversation.Ref.ID] = conversation
	return conversation, true, nil
}

func (s *MemoryStore) GetConversation(_ context.Context, id runtimesdk.ConversationID) (Conversation, error) {
	if s == nil {
		return Conversation{}, errors.New("memory store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	conversation, ok := s.conversations[id]
	if !ok {
		return Conversation{}, fmt.Errorf("%w: %s", ErrConversationNotFound, id)
	}
	return conversation, nil
}

func (s *MemoryStore) PutRun(_ context.Context, record RunRecord) (RunRecord, bool, error) {
	if s == nil {
		return RunRecord{}, false, errors.New("memory store is nil")
	}
	if record.ID == "" || record.ConversationID == "" || record.AgentID == "" || record.InputDigest == "" || record.CreatedAt.IsZero() {
		return RunRecord{}, false, errors.New("run record is incomplete")
	}
	if record.Handle.ID != record.ID {
		return RunRecord{}, false, errors.New("run record handle id mismatch")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.runs[record.ID]
	if ok {
		if existing.ConversationID != record.ConversationID || existing.AgentID != record.AgentID ||
			existing.InputDigest != record.InputDigest || existing.Handle.Fingerprint != record.Handle.Fingerprint {
			return RunRecord{}, false, fmt.Errorf("%w: run %s", ErrConflict, record.ID)
		}
		return existing, false, nil
	}
	conversation, ok := s.conversations[record.ConversationID]
	if !ok {
		return RunRecord{}, false, fmt.Errorf("%w: %s", ErrConversationNotFound, record.ConversationID)
	}
	if conversation.Ref.AgentID != record.AgentID {
		return RunRecord{}, false, fmt.Errorf("%w: run %s agent does not match conversation", ErrConflict, record.ID)
	}
	s.runs[record.ID] = record
	return record, true, nil
}

func (s *MemoryStore) GetRun(_ context.Context, id runtimesdk.RunID) (RunRecord, error) {
	if s == nil {
		return RunRecord{}, errors.New("memory store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.runs[id]
	if !ok {
		return RunRecord{}, fmt.Errorf("%w: %s", ErrRunNotFound, id)
	}
	return record, nil
}

func (s *MemoryStore) AppendEvent(_ context.Context, event runtimesdk.EventEnvelope) (runtimesdk.EventEnvelope, error) {
	if s == nil {
		return runtimesdk.EventEnvelope{}, errors.New("memory store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.conversations[event.ConversationID]; !ok {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: %s", ErrConversationNotFound, event.ConversationID)
	}
	record, ok := s.runs[event.RunID]
	if !ok {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: %s", ErrRunNotFound, event.RunID)
	}
	if record.ConversationID != event.ConversationID {
		return runtimesdk.EventEnvelope{}, fmt.Errorf("%w: run %s does not belong to conversation %s", ErrConflict, event.RunID, event.ConversationID)
	}
	history := s.events[event.ConversationID]
	event.Sequence = uint64(len(history) + 1)
	if err := event.Validate(); err != nil {
		return runtimesdk.EventEnvelope{}, err
	}
	s.events[event.ConversationID] = append(history, event)
	for _, notify := range s.watchers[event.ConversationID] {
		select {
		case notify <- struct{}{}:
		default:
		}
	}
	return event, nil
}

func (s *MemoryStore) ListEvents(_ context.Context, conversationID runtimesdk.ConversationID, after uint64, limit int) ([]runtimesdk.EventEnvelope, error) {
	if s == nil {
		return nil, errors.New("memory store is nil")
	}
	if limit <= 0 {
		return nil, errors.New("event list limit must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.conversations[conversationID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrConversationNotFound, conversationID)
	}
	history := s.events[conversationID]
	if after >= uint64(len(history)) {
		return nil, nil
	}
	start := int(after)
	end := start + limit
	if end > len(history) {
		end = len(history)
	}
	out := make([]runtimesdk.EventEnvelope, end-start)
	copy(out, history[start:end])
	return out, nil
}

func (s *MemoryStore) Watch(ctx context.Context, conversationID runtimesdk.ConversationID) (<-chan struct{}, func(), error) {
	if s == nil {
		return nil, nil, errors.New("memory store is nil")
	}
	s.mu.Lock()
	if _, ok := s.conversations[conversationID]; !ok {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: %s", ErrConversationNotFound, conversationID)
	}
	s.nextWatcher++
	id := s.nextWatcher
	ch := make(chan struct{}, 1)
	if s.watchers[conversationID] == nil {
		s.watchers[conversationID] = make(map[uint64]chan struct{})
	}
	s.watchers[conversationID][id] = ch
	s.mu.Unlock()

	var once sync.Once
	stopped := make(chan struct{})
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			if watchers := s.watchers[conversationID]; watchers != nil {
				delete(watchers, id)
				if len(watchers) == 0 {
					delete(s.watchers, conversationID)
				}
			}
			s.mu.Unlock()
			close(stopped)
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-stopped:
		}
	}()
	return ch, cancel, nil
}
