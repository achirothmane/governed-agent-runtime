import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

const manifestUrl = new URL("./upstream-contract.json", import.meta.url);
const manifest = JSON.parse(await readFile(manifestUrl, "utf8"));

function gitBlobSha1(value) {
  const bytes = Buffer.from(value, "utf8");
  return createHash("sha1")
    .update(Buffer.from("blob " + bytes.length + "\\0", "utf8"))
    .update(bytes)
    .digest("hex");
}

for (const source of manifest.sources) {
  const url = "https://raw.githubusercontent.com/" + manifest.upstream.repository + "/" + manifest.upstream.commit + "/" + source.path;
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error("Unable to fetch pinned upstream source " + source.path + ": " + response.status);
  }

  const body = await response.text();
  const actualSha = gitBlobSha1(body);
  if (actualSha !== source.gitBlobSha1) {
    throw new Error("Pinned blob mismatch for " + source.path + ": expected " + source.gitBlobSha1 + ", got " + actualSha);
  }

  for (const required of source.requires) {
    if (!body.includes(required)) {
      throw new Error("Missing required Trigger.dev contract fragment in " + source.path + ": " + required);
    }
  }

  process.stdout.write("PASS " + source.path + " " + actualSha + "\\n");
}

process.stdout.write("PASS upstream " + manifest.upstream.repository + "@" + manifest.upstream.commit + " SDK " + manifest.upstream.sdkVersion + "\\n");
