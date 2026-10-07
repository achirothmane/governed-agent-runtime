package temporaltools

import (
	"testing"

	"go.temporal.io/sdk/client"
)

type nilClient struct {
	client.Client
}

func TestNewWorkerValidatesComposition(t *testing.T) {
	cases := []struct {
		name   string
		config WorkerConfig
	}{
		{name: "missing client", config: WorkerConfig{TaskQueue: "q", Store: newMemoryInvocationStore(), Invoker: &countingInvoker{}}},
		{name: "missing queue", config: WorkerConfig{Client: nilClient{}, Store: newMemoryInvocationStore(), Invoker: &countingInvoker{}}},
		{name: "missing store", config: WorkerConfig{Client: nilClient{}, TaskQueue: "q", Invoker: &countingInvoker{}}},
		{name: "missing invoker", config: WorkerConfig{Client: nilClient{}, TaskQueue: "q", Store: newMemoryInvocationStore()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWorker(tc.config); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
