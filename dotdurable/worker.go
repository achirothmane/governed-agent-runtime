package dotdurable

import (
	"errors"
	"strings"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

type WorkerConfig struct {
	Client    client.Client
	TaskQueue string
	Snapshots SnapshotLoader
	Reasoner  Reasoner
	Store     Store
	Options   worker.Options
}

func NewWorker(config WorkerConfig) (worker.Worker, error) {
	if config.Client == nil {
		return nil, errors.New("Temporal client is required")
	}
	if strings.TrimSpace(config.TaskQueue) == "" {
		return nil, errors.New("Dot decision task queue is required")
	}
	if config.Snapshots == nil || config.Reasoner == nil || config.Store == nil {
		return nil, errors.New("durable Dot decision worker composition is incomplete")
	}
	w := worker.New(config.Client, config.TaskQueue, config.Options)
	Register(w, DecideActivity{
		Snapshots: config.Snapshots,
		Reasoner:  config.Reasoner,
		Store:     config.Store,
	})
	return w, nil
}
