/**
 * Thin adapter for Trigger.dev 4.x waitpoints.
 *
 * Import is dynamic so the deterministic contract tests do not require the SDK.
 * A live Trigger.dev task can call createTriggerWaitpointAdapter() and pass the
 * returned object to governedEffect().
 */
export async function createTriggerWaitpointAdapter() {
  const { wait } = await import("@trigger.dev/sdk");

  return {
    createToken(options) {
      return wait.createToken(options);
    },

    retrieveToken(tokenId) {
      return wait.retrieveToken(tokenId);
    },

    completeToken(tokenId, output) {
      return wait.completeToken(tokenId, output);
    },

    async waitForToken(tokenId) {
      const result = await wait.forToken(tokenId);
      if (!result.ok) {
        return {
          status: "TIMED_OUT",
          reason: result.error instanceof Error ? result.error.message : "WAITPOINT_TIMED_OUT",
        };
      }

      return {
        status: "COMPLETED",
        output: result.output,
      };
    },
  };
}
