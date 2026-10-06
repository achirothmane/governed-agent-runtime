/**
 * Trigger.dev governed-effect experiment.
 *
 * This module deliberately does not claim exactly-once execution. It gives a
 * registered effect a fail-closed recovery path: every retry reconciles the
 * destination before the effect can be dispatched again.
 */

export const Observation = Object.freeze({
  APPLIED_ONCE: "APPLIED_ONCE",
  ABSENT: "ABSENT",
  UNKNOWN: "UNKNOWN",
  DIVERGENT: "DIVERGENT",
});

export const Execution = Object.freeze({
  DISPATCHED: "DISPATCHED",
  DENIED: "DENIED",
  PROVEN_NOT_APPLIED: "PROVEN_NOT_APPLIED",
});

function requireFunction(object, name, owner) {
  if (!object || typeof object[name] !== "function") {
    throw new TypeError(`${owner}.${name} must be a function`);
  }
}

function assertContract({ effectId, waitpoints, destination }) {
  if (typeof effectId !== "string" || effectId.length === 0) {
    throw new TypeError("effectId must be a non-empty string");
  }

  for (const method of ["createToken", "retrieveToken", "completeToken"]) {
    requireFunction(waitpoints, method, "waitpoints");
  }

  for (const method of ["observe", "executeGuarded"]) {
    requireFunction(destination, method, "destination");
  }
}

function completedDecision(effectId, output) {
  if (!output || output.version !== 1 || output.effectId !== effectId) {
    return {
      outcome: "UNKNOWN",
      effectId,
      reason: "COMPLETED_WAITPOINT_WITHOUT_VALID_GOVERNED_RECEIPT",
    };
  }

  switch (output.decision) {
    case "CLOSED":
      return {
        outcome: "CLOSED",
        effectId,
        result: output.result,
        source: "WAITPOINT_RECEIPT",
      };
    case "RETRY_ALLOWED":
      return {
        outcome: "RETRY_ALLOWED",
        effectId,
        reason: output.reason ?? "RECONCILER_PROVED_ABSENCE",
      };
    case "DIVERGENT":
      return {
        outcome: "DIVERGENT",
        effectId,
        reason: output.reason ?? "RECONCILER_FOUND_DIVERGENCE",
      };
    default:
      return {
        outcome: "UNKNOWN",
        effectId,
        reason: "UNRECOGNIZED_GOVERNED_RECEIPT",
      };
  }
}

async function closeApplied({ effectId, token, waitpoints, observation }) {
  const receipt = {
    version: 1,
    effectId,
    decision: "CLOSED",
    result: observation.result,
    evidence: observation.evidence,
  };

  await waitpoints.completeToken(token.id, receipt);

  return {
    outcome: "CLOSED",
    effectId,
    result: observation.result,
    evidence: observation.evidence,
    source: "DESTINATION_OBSERVATION",
  };
}

async function hold({ effectId, token, waitpoints, reason }) {
  if (typeof waitpoints.waitForToken !== "function") {
    return {
      outcome: "HOLD",
      effectId,
      tokenId: token.id,
      reason,
    };
  }

  const resolved = await waitpoints.waitForToken(token.id);

  if (!resolved || resolved.status !== "COMPLETED") {
    return {
      outcome: "UNKNOWN",
      effectId,
      tokenId: token.id,
      reason: resolved?.reason ?? "RECONCILIATION_WAIT_DID_NOT_CLOSE",
    };
  }

  return completedDecision(effectId, resolved.output);
}

async function classifyObservation({ effectId, token, waitpoints, observation }) {
  switch (observation.kind) {
    case Observation.APPLIED_ONCE:
      return closeApplied({ effectId, token, waitpoints, observation });
    case Observation.ABSENT:
      return { outcome: "ABSENT", effectId };
    case Observation.UNKNOWN:
      return hold({
        effectId,
        token,
        waitpoints,
        reason: observation.reason ?? "DESTINATION_OUTCOME_UNKNOWN",
      });
    case Observation.DIVERGENT:
      return {
        outcome: "DIVERGENT",
        effectId,
        reason: observation.reason ?? "MULTIPLE_OR_CONFLICTING_EFFECTS",
      };
    default:
      return {
        outcome: "UNKNOWN",
        effectId,
        reason: "INVALID_DESTINATION_OBSERVATION",
      };
  }
}

/**
 * Execute one governed effect.
 *
 * Boundary:
 * - only calls routed through this function receive these semantics;
 * - destination.observe() is authoritative for APPLIED_ONCE / ABSENT claims;
 * - executeGuarded() must enforce the destination-native preconditions;
 * - UNKNOWN never grants retry authority.
 */
export async function governedEffect({
  effectId,
  waitpoints,
  destination,
  timeout = "24h",
  idempotencyKeyTTL = "30d",
}) {
  assertContract({ effectId, waitpoints, destination });

  // Reservation is durable and idempotent. It must happen before dispatch.
  const token = await waitpoints.createToken({
    idempotencyKey: effectId,
    idempotencyKeyTTL,
    timeout,
    tags: ["governed-effect"],
  });

  const tokenState = await waitpoints.retrieveToken(token.id);

  if (tokenState.status === "COMPLETED") {
    return completedDecision(effectId, tokenState.output);
  }

  if (tokenState.status === "TIMED_OUT") {
    return {
      outcome: "UNKNOWN",
      effectId,
      tokenId: token.id,
      reason: "RECONCILIATION_TOKEN_TIMED_OUT",
    };
  }

  // Critical rule: even a fresh token observes first. Token retention is not
  // treated as the source of truth for whether a physical effect happened.
  const before = await destination.observe(effectId);
  const beforeDecision = await classifyObservation({
    effectId,
    token,
    waitpoints,
    observation: before,
  });

  if (beforeDecision.outcome !== "ABSENT") {
    return beforeDecision;
  }

  let dispatch;
  try {
    dispatch = await destination.executeGuarded(effectId);
  } catch (error) {
    // An exception after dispatch is ambiguous by default. Reconcile before any
    // future execution authority is considered.
    const afterException = await destination.observe(effectId);
    const reconciled = await classifyObservation({
      effectId,
      token,
      waitpoints,
      observation: afterException,
    });

    if (reconciled.outcome === "ABSENT") {
      return {
        outcome: "RETRY_ALLOWED",
        effectId,
        reason: "TRUSTED_OBSERVER_PROVED_EFFECT_ABSENT_AFTER_DISPATCH_ERROR",
      };
    }

    return reconciled;
  }

  if (!dispatch || typeof dispatch.kind !== "string") {
    return {
      outcome: "UNKNOWN",
      effectId,
      reason: "INVALID_EXECUTION_RESULT",
    };
  }

  if (dispatch.kind === Execution.DENIED) {
    return {
      outcome: "DENIED",
      effectId,
      reason: dispatch.reason ?? "DESTINATION_NATIVE_GUARD_DENIED",
    };
  }

  if (dispatch.kind === Execution.PROVEN_NOT_APPLIED) {
    return {
      outcome: "RETRY_ALLOWED",
      effectId,
      reason: dispatch.reason ?? "DESTINATION_PROVED_NO_EFFECT",
    };
  }

  if (dispatch.kind !== Execution.DISPATCHED) {
    return {
      outcome: "UNKNOWN",
      effectId,
      reason: "UNRECOGNIZED_EXECUTION_RESULT",
    };
  }

  // Never claim success from the dispatch return alone.
  const after = await destination.observe(effectId);
  const afterDecision = await classifyObservation({
    effectId,
    token,
    waitpoints,
    observation: after,
  });

  if (afterDecision.outcome === "ABSENT") {
    return {
      outcome: "UNKNOWN",
      effectId,
      reason: "DISPATCH_REPORTED_BUT_EFFECT_NOT_OBSERVED",
    };
  }

  return afterDecision;
}
