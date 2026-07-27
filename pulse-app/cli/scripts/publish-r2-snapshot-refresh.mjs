#!/usr/bin/env node

import { writeFileSync } from 'node:fs';
import { isAbsolute, resolve } from 'node:path';

import { canonicalReleaseJSON } from '../src/release-manifest.js';
import { awsR2Client, publishR2SnapshotRefresh } from './publish-r2-release.mjs';

function fail() { throw new Error('r2_snapshot_refresh_arguments_invalid'); }

const values = {};
for (let index = 2; index < process.argv.length; index += 2) {
  const name = process.argv[index];
  const value = process.argv[index + 1];
  if (!['--artifact-set', '--bucket', '--endpoint', '--out', '--snapshot'].includes(name) ||
      !value || Object.hasOwn(values, name)) fail();
  values[name] = value;
}
if (Object.keys(values).length !== 5 ||
    ![values['--artifact-set'], values['--out'], values['--snapshot']].every((path) => isAbsolute(path))) fail();
const receipt = await publishR2SnapshotRefresh({
  artifactSetPath: resolve(values['--artifact-set']),
  client: awsR2Client({ bucket: values['--bucket'], endpoint: values['--endpoint'] }),
  snapshotPath: resolve(values['--snapshot']),
});
writeFileSync(resolve(values['--out']), `${canonicalReleaseJSON(receipt)}\n`, {
  encoding: 'utf8', flag: 'wx', mode: 0o600,
});
process.stdout.write(`${canonicalReleaseJSON(receipt)}\n`);
