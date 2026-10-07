package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"

	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/integrations/dataengine"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	databaseURL, err := requiredEnv("DATABASE_URL")
	if err != nil {
		return err
	}
	dataEngineURL, err := requiredEnv("DATA_ENGINE_MCP_URL")
	if err != nil {
		return err
	}

	temporalAddress := envOr("TEMPORAL_ADDRESS", client.DefaultHostPort)
	temporalNamespace := envOr("TEMPORAL_NAMESPACE", "default")
	taskQueue := envOr("TEMPORAL_TOOL_TASK_QUEUE", "ai-native-tools")

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	store, err := agentserver.NewPostgresStore(db)
	if err != nil {
		return err
	}
	if err := store.Migrate(ctx); err != nil {
		return err
	}

	temporalClient, err := client.Dial(client.Options{
		HostPort:  temporalAddress,
		Namespace: temporalNamespace,
	})
	if err != nil {
		return fmt.Errorf("dial temporal: %w", err)
	}
	defer temporalClient.Close()

	invoker := dataengine.Provider{
		Endpoint:  dataEngineURL,
		Connector: mcptransport.Connector{},
	}
	evidence := agentserver.ToolActivityEvidence{Store: store}
	w, err := temporaltools.NewWorker(temporaltools.WorkerConfig{
		Client:    temporalClient,
		TaskQueue: taskQueue,
		Store:     store,
		Invoker:   invoker,
		Evidence:  evidence,
	})
	if err != nil {
		return err
	}

	log.Printf(
		"ai-native tool worker started: temporal=%s namespace=%s queue=%s data_engine=%s",
		temporalAddress,
		temporalNamespace,
		taskQueue,
		dataEngineURL,
	)
	if err := w.Run(temporalworker.InterruptCh()); err != nil && !errors.Is(err, temporalworker.ErrWorkerShutdown) {
		return fmt.Errorf("run temporal worker: %w", err)
	}
	return nil
}

func requiredEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func envOr(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}
