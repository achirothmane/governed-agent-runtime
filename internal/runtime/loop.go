package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Handler interface {
	Handle(context.Context, ClaimedWork) error
}

type HandlerFunc func(context.Context, ClaimedWork) error

func (f HandlerFunc) Handle(ctx context.Context, work ClaimedWork) error {
	return f(ctx, work)
}

type Loop struct {
	Store    Store
	LeaseTTL time.Duration
	Now      func() time.Time
}

func (l Loop) RunOnce(ctx context.Context, workerID string, handler Handler) (bool, error) {
	if l.Store == nil {
		return false, errors.New("runtime store is required")
	}
	if handler == nil {
		return false, errors.New("runtime handler is required")
	}
	if l.LeaseTTL <= 0 {
		return false, errors.New("runtime lease ttl must be positive")
	}

	now := time.Now
	if l.Now != nil {
		now = l.Now
	}

	claimed, err := l.Store.Claim(ctx, workerID, now(), l.LeaseTTL)
	if errors.Is(err, ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim work: %w", err)
	}

	if err := handler.Handle(ctx, claimed); err != nil {
		return true, fmt.Errorf("handle event %s: %w", claimed.Record.Event.ID, err)
	}

	if _, err := l.Store.Complete(ctx, claimed.Lease, now()); err != nil {
		return true, fmt.Errorf("complete event %s: %w", claimed.Record.Event.ID, err)
	}
	return true, nil
}
