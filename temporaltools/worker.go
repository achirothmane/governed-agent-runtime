package temporaltools

import (
	"errors"
	"strings"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

type WorkerConfig struct {
	Client    client.Client
	TaskQueue string
	Store     InvocationStore
	Invoker   ToolInvoker
	Evidence  EvidenceSink
	Options   worker.Options
}

func NewWorker(config WorkerConfig) (worker.Worker, error) {
	if config.Client == nil {
		return nil, errors.New("temporal client is required")
	}
	if strings.TrimSpace(config.TaskQueue) == "" {
		return nil, errors.New("temporal tool task queue is required")
	}
	if config.Store == nil {
		return nil, errors.New("tool invocation store is required")
	}
	if config.Invoker == nil {
		return nil, errors.New("tool invoker is required")
	}

	w := worker.New(config.Client, config.TaskQueue, config.Options)
	Register(w, Activity{
		Store:    config.Store,
		Invoker:  config.Invoker,
		Evidence: config.Evidence,
	})
	return w, nil
}
