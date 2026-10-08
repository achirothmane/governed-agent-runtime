package temporalagent

import (
	"errors"
	"strings"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

type WorkerConfig struct {
	Client       client.Client
	TaskQueue    string
	Resolver     RunResolver
	Reasoner     agentloop.Reasoner
	Executions   ExecutionStore
	Steps        StepStore
	Invocations  temporaltools.InvocationStore
	ToolExecutor agentloop.ToolExecutor
	Options      worker.Options
}

func NewWorker(config WorkerConfig) (worker.Worker, error) {
	if config.Client == nil {
		return nil, errors.New("temporal client is required")
	}
	if strings.TrimSpace(config.TaskQueue) == "" {
		return nil, errors.New("temporal agent task queue is required")
	}
	if config.Resolver == nil || config.Reasoner == nil ||
		config.Executions == nil || config.Steps == nil ||
		config.Invocations == nil || config.ToolExecutor == nil {
		return nil, errors.New("temporal agent worker composition is incomplete")
	}

	w := worker.New(config.Client, config.TaskQueue, config.Options)
	Register(
		w,
		ReasonActivity{
			Resolver:    config.Resolver,
			Reasoner:    config.Reasoner,
			Executions:  config.Executions,
			Steps:       config.Steps,
			Invocations: config.Invocations,
		},
		ToolDispatchActivity{
			Invocations: config.Invocations,
			Executor:    config.ToolExecutor,
		},
	)
	return w, nil
}
