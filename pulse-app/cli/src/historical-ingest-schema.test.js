import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import test from "node:test";

import {
  historicalIngestSchemaBytes,
  historicalIngestSchemaDigest,
  historicalIngestSchema,
} from "./historical-ingest-schema.js";

const canonicalSchemaUrl = new URL(
  "../../internal/historicalingest/schema/historical_ingest_v1.schema.json",
  import.meta.url,
);

test("packaged historical ingest schema matches Go embedded artifact", async () => {
  const canonical = await readFile(canonicalSchemaUrl);
  const packaged = historicalIngestSchemaBytes();
  assert.deepEqual(packaged, canonical);
  assert.equal(
    historicalIngestSchemaDigest(),
    createHash("sha256").update(canonical).digest("hex"),
  );
});

test("historical ingest schema is closed", () => {
  const schema = historicalIngestSchema();
  assert.equal(schema.$id, "https://zbs.gg/schemas/pulse/historical-ingest/v1");
  assert.equal(schema.additionalProperties, false);
  assert.equal(schema.$defs.materialItem.additionalProperties, false);
  assert.equal(schema.$defs.payload.additionalProperties, false);
});
