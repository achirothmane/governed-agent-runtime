import pg from "pg";

const { Client } = pg;
const databaseUrl = process.env.GOV_EFFECT_DATABASE_URL;
const effectId = process.env.GOV_EFFECT_ID;

if (!databaseUrl || !effectId) {
  throw new Error("GOV_EFFECT_DATABASE_URL and GOV_EFFECT_ID are required");
}

const client = new Client({ connectionString: databaseUrl });
await client.connect();

try {
  await client.query("BEGIN");

  const boundary = await client.query(
    `SELECT owner_id, generation, authority_live, semantic_state
     FROM "GovEffectTarget"
     WHERE target_id = 'account:1'
     FOR UPDATE`
  );
  const row = boundary.rows[0];

  if (
    !row ||
    row.owner_id !== "worker-b" ||
    Number(row.generation) !== 2 ||
    row.authority_live !== true ||
    row.semantic_state !== "balance:v7"
  ) {
    throw new Error("native boundary denied child effect");
  }

  const existing = await client.query(
    `SELECT count(*)::int AS count
     FROM "GovEffectReceipt"
     WHERE effect_id = $1`,
    [effectId]
  );
  if (Number(existing.rows[0]?.count ?? 0) !== 0) {
    throw new Error("effect already committed");
  }

  const inserted = await client.query(
    `INSERT INTO "GovEffectLedger" (effect_id, target_id, amount)
     VALUES ($1, 'account:1', 100)
     RETURNING id`,
    [effectId]
  );
  const ledgerId = Number(inserted.rows[0].id);
  const result = { effectId, ledgerId, amount: 100, worker: "child-process" };

  await client.query(
    `INSERT INTO "GovEffectReceipt" (effect_id, target_id, ledger_id, result)
     VALUES ($1, 'account:1', $2, $3::jsonb)`,
    [effectId, ledgerId, JSON.stringify(result)]
  );

  await client.query("COMMIT");

  // This is the exact kill seam: the parent kills this process after observing
  // COMMITTED and before this worker can close any Trigger.dev waitpoint.
  process.stdout.write("COMMITTED\n");

  // Stay alive until the parent delivers SIGKILL. No cleanup/ack path is run.
  setInterval(() => {}, 60_000);
} catch (error) {
  try {
    await client.query("ROLLBACK");
  } catch {}
  console.error(error);
  process.exitCode = 1;
  await client.end();
}
