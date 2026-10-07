import assert from "node:assert/strict";
import test from "node:test";
import { governedEffect, Observation, Execution } from "./governed-effect.mjs";

class MemoryWaitpoints {
  constructor() {
    this.byKey = new Map();
    this.byId = new Map();
    this.nextId = 1;
    this.waiters = new Map();
  }

  async createToken({ idempotencyKey }) {
    const existing = this.byKey.get(idempotencyKey);
    if (existing) {
      return { id: existing.id, isCached: true };
    }

    const token = {
      id: `waitpoint_${this.nextId++}`,
      idempotencyKey,
      status: "WAITING",
      output: undefined,
    };
    this.byKey.set(idempotencyKey, token);
    this.byId.set(token.id, token);
    return { id: token.id, isCached: false };
  }

  async retrieveToken(id) {
    const token = this.byId.get(id);
    if (!token) throw new Error("missing token");
    return structuredClone(token);
  }

  async completeToken(id, output) {
    const token = this.byId.get(id);
    if (!token) throw new Error("missing token");

    if (token.status === "COMPLETED") {
      return { success: true };
    }

    token.status = "COMPLETED";
    token.output = structuredClone(output);

    const waiters = this.waiters.get(id) ?? [];
    this.waiters.delete(id);
    for (const resolve of waiters) {
      resolve({ status: "COMPLETED", output: structuredClone(output) });
    }

    return { success: true };
  }

  async waitForToken(id) {
    const token = this.byId.get(id);
    if (!token) throw new Error("missing token");
    if (token.status === "COMPLETED") {
      return { status: "COMPLETED", output: structuredClone(token.output) };
    }

    return new Promise((resolve) => {
      const waiters = this.waiters.get(id) ?? [];
      waiters.push(resolve);
      this.waiters.set(id, waiters);
    });
  }

  async seedWaiting(effectId) {
    return this.createToken({ idempotencyKey: effectId });
  }

  async seedCompleted(effectId, result) {
    const token = await this.createToken({ idempotencyKey: effectId });
    await this.completeToken(token.id, {
      version: 1,
      effectId,
      decision: "CLOSED",
      result,
    });
    return token;
  }
}

class MemoryDestination {
  constructor() {
    this.effects = new Map();
    this.executeCalls = 0;
    this.observeCalls = 0;
    this.mode = "normal";
    this.observeOverride = undefined;
  }

  async observe(effectId) {
    this.observeCalls += 1;

    if (this.observeOverride) {
      return this.observeOverride(effectId, this);
    }

    const rows = this.effects.get(effectId) ?? [];
    if (rows.length === 0) return { kind: Observation.ABSENT };
    if (rows.length === 1) {
      return {
        kind: Observation.APPLIED_ONCE,
        result: rows[0],
        evidence: { source: "memory-ledger", cardinality: 1 },
      };
    }
    return { kind: Observation.DIVERGENT, reason: "CARDINALITY_GT_ONE" };
  }

  async executeGuarded(effectId) {
    this.executeCalls += 1;

    if (this.mode === "denied") {
      return { kind: Execution.DENIED, reason: "STALE_AUTHORITY_OR_GENERATION" };
    }

    if (this.mode === "proven-not-applied") {
      return { kind: Execution.PROVEN_NOT_APPLIED, reason: "PRECONDITION_CHANGED" };
    }

    const rows = this.effects.get(effectId) ?? [];
    if (rows.length > 0) {
      return { kind: Execution.DENIED, reason: "EFFECT_ID_ALREADY_PRESENT" };
    }

    const result = { effectId, physicalSequence: 1 };
    rows.push(result);
    this.effects.set(effectId, rows);

    if (this.mode === "crash-after-effect") {
      throw new Error("simulated lost acknowledgement after physical effect");
    }

    return { kind: Execution.DISPATCHED };
  }

  seedApplied(effectId, result = { effectId, physicalSequence: 1 }) {
    this.effects.set(effectId, [result]);
  }

  seedDivergent(effectId) {
    this.effects.set(effectId, [
      { effectId, physicalSequence: 1 },
      { effectId, physicalSequence: 2 },
    ]);
  }

  count(effectId) {
    return (this.effects.get(effectId) ?? []).length;
  }
}

test("normal path closes only after post-effect observation", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();

  const result = await governedEffect({
    effectId: "effect:normal",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "CLOSED");
  assert.equal(destination.count("effect:normal"), 1);
  assert.equal(destination.executeCalls, 1);
  assert.ok(destination.observeCalls >= 2);
});

test("restart after reservation but before effect executes exactly once", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  await waitpoints.seedWaiting("effect:pre-dispatch-crash");

  const result = await governedEffect({
    effectId: "effect:pre-dispatch-crash",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "CLOSED");
  assert.equal(destination.count("effect:pre-dispatch-crash"), 1);
  assert.equal(destination.executeCalls, 1);
});

test("restart after physical effect but before token completion reconciles without redispatch", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  await waitpoints.seedWaiting("effect:post-dispatch-crash");
  destination.seedApplied("effect:post-dispatch-crash");

  const result = await governedEffect({
    effectId: "effect:post-dispatch-crash",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "CLOSED");
  assert.equal(destination.count("effect:post-dispatch-crash"), 1);
  assert.equal(destination.executeCalls, 0);
});

test("lost acknowledgement inside execute reconciles the applied effect", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  destination.mode = "crash-after-effect";

  const result = await governedEffect({
    effectId: "effect:lost-ack",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "CLOSED");
  assert.equal(destination.count("effect:lost-ack"), 1);
  assert.equal(destination.executeCalls, 1);
});

test("completed waitpoint replay returns receipt and never touches destination", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  await waitpoints.seedCompleted("effect:completed", { value: 42 });

  const result = await governedEffect({
    effectId: "effect:completed",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "CLOSED");
  assert.deepEqual(result.result, { value: 42 });
  assert.equal(destination.executeCalls, 0);
  assert.equal(destination.observeCalls, 0);
});

test("UNKNOWN holds and grants no execution authority until reconciliation completes", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  destination.observeOverride = () => ({
    kind: Observation.UNKNOWN,
    reason: "OBSERVER_UNAVAILABLE",
  });

  const pending = governedEffect({
    effectId: "effect:unknown",
    waitpoints,
    destination,
  });

  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(destination.executeCalls, 0);

  const token = waitpoints.byKey.get("effect:unknown");
  await waitpoints.completeToken(token.id, {
    version: 1,
    effectId: "effect:unknown",
    decision: "CLOSED",
    result: { recovered: true },
  });

  const result = await pending;
  assert.equal(result.outcome, "CLOSED");
  assert.equal(destination.executeCalls, 0);
});

test("trusted proof of absence after a dispatch error yields retry authority, not an immediate replay", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  let observations = 0;

  destination.observeOverride = () => {
    observations += 1;
    return { kind: Observation.ABSENT };
  };
  destination.executeGuarded = async () => {
    destination.executeCalls += 1;
    throw new Error("dispatch channel failed before effect");
  };

  const result = await governedEffect({
    effectId: "effect:proved-absent",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "RETRY_ALLOWED");
  assert.equal(destination.executeCalls, 1);
  assert.equal(observations, 2);
});

test("stale authority or generation is denied without effect", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  destination.mode = "denied";

  const result = await governedEffect({
    effectId: "effect:stale-owner",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "DENIED");
  assert.equal(destination.count("effect:stale-owner"), 0);
  assert.equal(destination.executeCalls, 1);
});

test("same effect replay closes from the durable receipt and preserves cardinality one", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();

  const first = await governedEffect({
    effectId: "effect:replay",
    waitpoints,
    destination,
  });
  const second = await governedEffect({
    effectId: "effect:replay",
    waitpoints,
    destination,
  });

  assert.equal(first.outcome, "CLOSED");
  assert.equal(second.outcome, "CLOSED");
  assert.equal(destination.count("effect:replay"), 1);
  assert.equal(destination.executeCalls, 1);
});

test("divergent cardinality freezes instead of retrying", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  destination.seedDivergent("effect:divergent");

  const result = await governedEffect({
    effectId: "effect:divergent",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "DIVERGENT");
  assert.equal(destination.executeCalls, 0);
});

test("dispatch claim without observable effect fails closed", async () => {
  const waitpoints = new MemoryWaitpoints();
  const destination = new MemoryDestination();
  destination.executeGuarded = async () => {
    destination.executeCalls += 1;
    return { kind: Execution.DISPATCHED };
  };

  const result = await governedEffect({
    effectId: "effect:false-success",
    waitpoints,
    destination,
  });

  assert.equal(result.outcome, "UNKNOWN");
  assert.equal(result.reason, "DISPATCH_REPORTED_BUT_EFFECT_NOT_OBSERVED");
});
