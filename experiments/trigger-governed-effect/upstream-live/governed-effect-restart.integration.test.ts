import { trace } from "@opentelemetry/api";
import { RunEngine } from "@internal/run-engine";
import { setupAuthenticatedEnvironment } from "@internal/run-engine/tests";
import { containerTest } from "@internal/testcontainers";
import { expect, vi } from "vitest";
import { governedEffect, Observation, Execution } from "./governed-effect-fixture.mjs";

vi.setConfig({ testTimeout: 120_000 });

function buildEngine(prisma: any, redisOptions: any) {
  return new RunEngine({
    prisma,
    worker: { redis: redisOptions, workers: 1, tasksPerWorker: 10, pollIntervalMs: 100 },
    queue: { redis: redisOptions },
    runLock: { redis: redisOptions },
    machines: {
      defaultMachine: "small-1x",
      machines: {
        "small-1x": { name: "small-1x", cpu: 0.5, memory: 0.5, centsPerMs: 0.0001 },
      },
      baseCostInCents: 0.0005,
    },
    tracer: trace.getTracer("governed-effect-restart-gate", "0.0.0"),
  });
}

function waitpointAdapter(engine: RunEngine, env: any) {
  return {
    async createToken(options: any) {
      const result = await engine.createManualWaitpoint({
        environmentId: env.id,
        projectId: env.project.id,
        idempotencyKey: options.idempotencyKey,
        idempotencyKeyExpiresAt: new Date(Date.now() + 24 * 60 * 60 * 1000),
        timeout: new Date(Date.now() + 60_000),
      });
      return { id: result.waitpoint.id, isCached: result.isCached };
    },

    async retrieveToken(id: string) {
      const waitpoint = await engine.getWaitpoint({
        waitpointId: id,
        environmentId: env.id,
        projectId: env.project.id,
      });
      if (!waitpoint) throw new Error("waitpoint not found");

      let output: unknown = undefined;
      if (waitpoint.output) {
        output = JSON.parse(waitpoint.output);
      }

      return { status: waitpoint.status, output };
    },

    async completeToken(id: string, output: unknown) {
      await engine.completeWaitpoint({
        id,
        output: {
          value: JSON.stringify(output),
          type: "application/json",
          isError: false,
        },
      });
      return { success: true };
    },
  };
}

class PostgresDestination {
  executeCalls = 0;

  constructor(private readonly prisma: any) {}

  async reset() {
    // Prisma sends raw statements through PostgreSQL prepared statements, which
    // intentionally accept one command at a time. Keep setup explicit so this
    // gate tests the recovery protocol rather than a driver-specific SQL batch.
    const statements = [
      `DROP TABLE IF EXISTS "GovEffectReceipt"`,
      `DROP TABLE IF EXISTS "GovEffectLedger"`,
      `DROP TABLE IF EXISTS "GovEffectTarget"`,
      `CREATE TABLE "GovEffectTarget" (
        target_id text PRIMARY KEY,
        owner_id text NOT NULL,
        generation bigint NOT NULL,
        authority_live boolean NOT NULL,
        semantic_state text NOT NULL
      )`,
      `CREATE TABLE "GovEffectLedger" (
        id bigserial PRIMARY KEY,
        effect_id text NOT NULL,
        target_id text NOT NULL,
        amount bigint NOT NULL
      )`,
      `CREATE TABLE "GovEffectReceipt" (
        effect_id text PRIMARY KEY,
        target_id text NOT NULL,
        ledger_id bigint NOT NULL,
        result jsonb NOT NULL
      )`,
      `INSERT INTO "GovEffectTarget"
        (target_id, owner_id, generation, authority_live, semantic_state)
       VALUES
        ('account:1', 'worker-b', 2, true, 'balance:v7')`,
    ];

    for (const statement of statements) {
      await this.prisma.$executeRawUnsafe(statement);
    }
  }

  async observe(effectId: string) {
    const ledger = await this.prisma.$queryRawUnsafe<Array<{ count: bigint }>>(
      `SELECT count(*)::bigint AS count FROM "GovEffectLedger" WHERE effect_id = $1`,
      effectId
    );
    const receipts = await this.prisma.$queryRawUnsafe<Array<{ count: bigint; result: unknown }>>(
      `SELECT count(*)::bigint AS count, COALESCE(max(result::text), '')::text AS result
       FROM "GovEffectReceipt" WHERE effect_id = $1`,
      effectId
    );

    const ledgerCount = Number(ledger[0]?.count ?? 0n);
    const receiptCount = Number(receipts[0]?.count ?? 0n);

    if (ledgerCount === 0 && receiptCount === 0) {
      return { kind: Observation.ABSENT };
    }
    if (ledgerCount === 1 && receiptCount === 1) {
      const raw = receipts[0]?.result;
      const result = typeof raw === "string" && raw ? JSON.parse(raw) : raw;
      return {
        kind: Observation.APPLIED_ONCE,
        result,
        evidence: { source: "postgres-native-receipt", ledgerCount, receiptCount },
      };
    }
    return {
      kind: Observation.DIVERGENT,
      reason: `ledger=${ledgerCount},receipt=${receiptCount}`,
    };
  }

  async executeGuarded(effectId: string) {
    this.executeCalls += 1;

    return await this.prisma.$transaction(async (tx: any) => {
      const rows = await tx.$queryRawUnsafe<
        Array<{ owner_id: string; generation: bigint; authority_live: boolean; semantic_state: string }>
      >(
        `SELECT owner_id, generation, authority_live, semantic_state
         FROM "GovEffectTarget"
         WHERE target_id = 'account:1'
         FOR UPDATE`
      );
      const row = rows[0];
      if (
        !row ||
        row.owner_id !== "worker-b" ||
        Number(row.generation) !== 2 ||
        !row.authority_live ||
        row.semantic_state !== "balance:v7"
      ) {
        return { kind: Execution.DENIED, reason: "STALE_NATIVE_BOUNDARY" };
      }

      const existing = await tx.$queryRawUnsafe<Array<{ count: bigint }>>(
        `SELECT count(*)::bigint AS count FROM "GovEffectReceipt" WHERE effect_id = $1`,
        effectId
      );
      if (Number(existing[0]?.count ?? 0n) !== 0) {
        return { kind: Execution.DENIED, reason: "EFFECT_ALREADY_COMMITTED" };
      }

      const inserted = await tx.$queryRawUnsafe<Array<{ id: bigint }>>(
        `INSERT INTO "GovEffectLedger" (effect_id, target_id, amount)
         VALUES ($1, 'account:1', 100)
         RETURNING id`,
        effectId
      );
      const ledgerId = Number(inserted[0]!.id);
      const result = { effectId, ledgerId, amount: 100 };

      await tx.$executeRawUnsafe(
        `INSERT INTO "GovEffectReceipt" (effect_id, target_id, ledger_id, result)
         VALUES ($1, 'account:1', $2, $3::jsonb)`,
        effectId,
        ledgerId,
        JSON.stringify(result)
      );

      return { kind: Execution.DISPATCHED };
    });
  }

  async physicalCount(effectId: string) {
    const rows = await this.prisma.$queryRawUnsafe<Array<{ count: bigint }>>(
      `SELECT count(*)::bigint AS count FROM "GovEffectLedger" WHERE effect_id = $1`,
      effectId
    );
    return Number(rows[0]?.count ?? 0n);
  }
}

containerTest(
  "Trigger RunEngine restart reconciles committed native effect before redispatch",
  async ({ prisma, redisOptions }) => {
    const env = await setupAuthenticatedEnvironment(prisma, "PRODUCTION");
    const destination = new PostgresDestination(prisma);
    await destination.reset();

    const effectId = "effect:trigger-engine-restart";

    // First process: reserve the exact Trigger.dev waitpoint and pass the
    // pre-dispatch observation. Then commit the real PostgreSQL effect.
    const engine1 = buildEngine(prisma, redisOptions);
    const waits1 = waitpointAdapter(engine1, env);
    const reservation = await waits1.createToken({ idempotencyKey: effectId });
    expect(reservation.isCached).toBe(false);
    expect((await destination.observe(effectId)).kind).toBe(Observation.ABSENT);

    const dispatch = await destination.executeGuarded(effectId);
    expect(dispatch.kind).toBe(Execution.DISPATCHED);
    expect(await destination.physicalCount(effectId)).toBe(1);

    // Crash window: the native COMMIT exists but the waitpoint was never closed.
    // Quitting and reconstructing RunEngine proves recovery is not in-memory.
    await engine1.quit();

    const engine2 = buildEngine(prisma, redisOptions);
    try {
      const result = await governedEffect({
        effectId,
        waitpoints: waitpointAdapter(engine2, env),
        destination,
      });

      expect(result.outcome).toBe("CLOSED");
      expect(destination.executeCalls).toBe(1);
      expect(await destination.physicalCount(effectId)).toBe(1);

      const cached = await engine2.createManualWaitpoint({
        environmentId: env.id,
        projectId: env.project.id,
        idempotencyKey: effectId,
        idempotencyKeyExpiresAt: new Date(Date.now() + 24 * 60 * 60 * 1000),
      });
      expect(cached.isCached).toBe(true);
      expect(cached.waitpoint.id).toBe(reservation.id);

      const closed = await engine2.getWaitpoint({
        waitpointId: reservation.id,
        environmentId: env.id,
        projectId: env.project.id,
      });
      expect(closed?.status).toBe("COMPLETED");
      expect(closed?.output).toContain('"decision":"CLOSED"');
    } finally {
      await engine2.quit();
    }
  }
);
