package postgresproof

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

var (
	errDenied = errors.New("native effect denied")
	errReplay = errors.New("effect already committed")
)

type expectedBoundary struct {
	TargetID      string
	Owner         string
	Generation    int64
	SemanticState string
}

type observation string

const (
	absent      observation = "ABSENT"
	appliedOnce observation = "APPLIED_ONCE"
	divergent   observation = "DIVERGENT"
	unknown     observation = "UNKNOWN"
)

func connect(t *testing.T) *pgx.Conn {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is required for the PostgreSQL native-effect experiment")
	}

	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func resetSchema(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()

	_, err := conn.Exec(ctx, `
DROP TABLE IF EXISTS trigger_governed_effect_receipt;
DROP TABLE IF EXISTS trigger_governed_effect_ledger;
DROP TABLE IF EXISTS trigger_governed_effect_target;

CREATE TABLE trigger_governed_effect_target (
	target_id text PRIMARY KEY,
	owner_id text NOT NULL,
	generation bigint NOT NULL,
	authority_live boolean NOT NULL,
	semantic_state text NOT NULL
);

-- Deliberately NO UNIQUE(effect_id). A duplicate physical effect must stay visible.
CREATE TABLE trigger_governed_effect_ledger (
	id bigserial PRIMARY KEY,
	effect_id text NOT NULL,
	target_id text NOT NULL,
	amount bigint NOT NULL
);

CREATE TABLE trigger_governed_effect_receipt (
	effect_id text PRIMARY KEY,
	target_id text NOT NULL,
	owner_id text NOT NULL,
	generation bigint NOT NULL,
	semantic_state text NOT NULL,
	ledger_id bigint NOT NULL,
	result text NOT NULL
);

INSERT INTO trigger_governed_effect_target
	(target_id, owner_id, generation, authority_live, semantic_state)
VALUES
	('account:1', 'worker-b', 2, true, 'balance:v7');
`)
	if err != nil {
		t.Fatalf("reset schema: %v", err)
	}
}

func executeGuarded(
	ctx context.Context,
	conn *pgx.Conn,
	effectID string,
	expected expectedBoundary,
	amount int64,
) error {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var owner string
	var generation int64
	var authorityLive bool
	var semanticState string

	err = tx.QueryRow(ctx, `
SELECT owner_id, generation, authority_live, semantic_state
FROM trigger_governed_effect_target
WHERE target_id = $1
FOR UPDATE
`, expected.TargetID).Scan(&owner, &generation, &authorityLive, &semanticState)
	if err != nil {
		return err
	}

	if owner != expected.Owner ||
		generation != expected.Generation ||
		!authorityLive ||
		semanticState != expected.SemanticState {
		return errDenied
	}

	var existing int
	err = tx.QueryRow(ctx,
		`SELECT count(*) FROM trigger_governed_effect_receipt WHERE effect_id = $1`,
		effectID,
	).Scan(&existing)
	if err != nil {
		return err
	}
	if existing != 0 {
		return errReplay
	}

	var ledgerID int64
	err = tx.QueryRow(ctx, `
INSERT INTO trigger_governed_effect_ledger (effect_id, target_id, amount)
VALUES ($1, $2, $3)
RETURNING id
`, effectID, expected.TargetID, amount).Scan(&ledgerID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
INSERT INTO trigger_governed_effect_receipt
	(effect_id, target_id, owner_id, generation, semantic_state, ledger_id, result)
VALUES
	($1, $2, $3, $4, $5, $6, $7)
`,
		effectID,
		expected.TargetID,
		expected.Owner,
		expected.Generation,
		expected.SemanticState,
		ledgerID,
		fmt.Sprintf("ledger:%d", ledgerID),
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func observeEffect(ctx context.Context, conn *pgx.Conn, effectID string) (observation, string, error) {
	var ledgerCount int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM trigger_governed_effect_ledger WHERE effect_id = $1`,
		effectID,
	).Scan(&ledgerCount); err != nil {
		return unknown, "", err
	}

	var receiptCount int
	var result string
	err := conn.QueryRow(ctx, `
SELECT count(*), COALESCE(max(result), '')
FROM trigger_governed_effect_receipt
WHERE effect_id = $1
`, effectID).Scan(&receiptCount, &result)
	if err != nil {
		return unknown, "", err
	}

	switch {
	case ledgerCount == 0 && receiptCount == 0:
		return absent, "", nil
	case ledgerCount == 1 && receiptCount == 1:
		return appliedOnce, result, nil
	default:
		return divergent, "", nil
	}
}

func ledgerCount(t *testing.T, conn *pgx.Conn, effectID string) int {
	t.Helper()
	var count int
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM trigger_governed_effect_ledger WHERE effect_id = $1`,
		effectID,
	).Scan(&count); err != nil {
		t.Fatalf("ledger count: %v", err)
	}
	return count
}

func currentBoundary() expectedBoundary {
	return expectedBoundary{
		TargetID:      "account:1",
		Owner:         "worker-b",
		Generation:    2,
		SemanticState: "balance:v7",
	}
}

func TestPostgresNativeEffectBoundary(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	t.Run("commit then worker death is recovered without redispatch", func(t *testing.T) {
		resetSchema(t, conn)
		const effectID = "effect:commit-then-die"

		// Worker A/B equivalent: the physical non-idempotent effect and its receipt
		// commit atomically. The worker is then considered dead before any Trigger
		// waitpoint completion can be recorded.
		if err := executeGuarded(ctx, conn, effectID, currentBoundary(), 100); err != nil {
			t.Fatalf("first guarded execution: %v", err)
		}

		// Recovery MUST observe before it can consider another dispatch.
		got, result, err := observeEffect(ctx, conn, effectID)
		if err != nil {
			t.Fatalf("observe recovery: %v", err)
		}
		if got != appliedOnce {
			t.Fatalf("recovery observation = %s, want %s", got, appliedOnce)
		}
		if result == "" {
			t.Fatal("recovery lost exact committed result")
		}

		// The orchestrator closes from observation; it never calls executeGuarded
		// again. The deliberately unconstrained business ledger proves cardinality.
		if count := ledgerCount(t, conn, effectID); count != 1 {
			t.Fatalf("physical effect count = %d, want 1", count)
		}
	})

	t.Run("exact replay is denied and cannot create a second ledger row", func(t *testing.T) {
		resetSchema(t, conn)
		const effectID = "effect:replay"

		if err := executeGuarded(ctx, conn, effectID, currentBoundary(), 100); err != nil {
			t.Fatalf("first execution: %v", err)
		}
		if err := executeGuarded(ctx, conn, effectID, currentBoundary(), 100); !errors.Is(err, errReplay) {
			t.Fatalf("replay error = %v, want %v", err, errReplay)
		}
		if count := ledgerCount(t, conn, effectID); count != 1 {
			t.Fatalf("physical effect count = %d, want 1", count)
		}
	})

	t.Run("stale generation is denied before effect", func(t *testing.T) {
		resetSchema(t, conn)
		expected := currentBoundary()
		expected.Generation = 1

		if err := executeGuarded(ctx, conn, "effect:stale-generation", expected, 100); !errors.Is(err, errDenied) {
			t.Fatalf("stale generation error = %v, want %v", err, errDenied)
		}
		if count := ledgerCount(t, conn, "effect:stale-generation"); count != 0 {
			t.Fatalf("physical effect count = %d, want 0", count)
		}
	})

	t.Run("revoked authority is denied before effect", func(t *testing.T) {
		resetSchema(t, conn)
		if _, err := conn.Exec(ctx,
			`UPDATE trigger_governed_effect_target SET authority_live = false WHERE target_id = 'account:1'`,
		); err != nil {
			t.Fatalf("revoke authority: %v", err)
		}

		if err := executeGuarded(ctx, conn, "effect:revoked", currentBoundary(), 100); !errors.Is(err, errDenied) {
			t.Fatalf("revoked authority error = %v, want %v", err, errDenied)
		}
		if count := ledgerCount(t, conn, "effect:revoked"); count != 0 {
			t.Fatalf("physical effect count = %d, want 0", count)
		}
	})

	t.Run("semantic state drift is denied before effect", func(t *testing.T) {
		resetSchema(t, conn)
		if _, err := conn.Exec(ctx,
			`UPDATE trigger_governed_effect_target SET semantic_state = 'balance:v8' WHERE target_id = 'account:1'`,
		); err != nil {
			t.Fatalf("change state: %v", err)
		}

		if err := executeGuarded(ctx, conn, "effect:state-drift", currentBoundary(), 100); !errors.Is(err, errDenied) {
			t.Fatalf("state drift error = %v, want %v", err, errDenied)
		}
		if count := ledgerCount(t, conn, "effect:state-drift"); count != 0 {
			t.Fatalf("physical effect count = %d, want 0", count)
		}
	})
}
