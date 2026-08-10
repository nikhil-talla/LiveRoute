import { mkdtemp, readFile, rm } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const generated = join(root, "src/generated/liveroute-api.ts");
const temporaryDirectory = await mkdtemp(join(tmpdir(), "liveroute-openapi-"));
const candidate = join(temporaryDirectory, "liveroute-api.ts");

try {
  const result = spawnSync(
    join(root, "node_modules/.bin/openapi-typescript"),
    [
      resolve(root, "../schema/http/liveroute-v1.5.openapi.yaml"),
      "--output",
      candidate,
    ],
    { encoding: "utf8" },
  );
  if (result.status !== 0) {
    process.stderr.write(result.stderr);
    process.exit(result.status ?? 1);
  }
  const [expected, actual] = await Promise.all([
    readFile(candidate, "utf8"),
    readFile(generated, "utf8"),
  ]);
  if (expected !== actual) {
    console.error("Generated HTTP types are stale; run npm run generate:api.");
    process.exit(1);
  }
  console.log("Generated HTTP types match the normative OpenAPI contract.");
} finally {
  await rm(temporaryDirectory, { recursive: true, force: true });
}
