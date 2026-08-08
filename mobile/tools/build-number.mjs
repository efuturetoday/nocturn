#!/usr/bin/env node
// The build number for a release, derived from the version rather than counted.
//
// It has to satisfy two stores at once. Android wants versionCode STRICTLY INCREASING across every
// release forever — a lower or equal one simply refuses to install over what a tester already has.
// iOS wants CFBundleVersion strictly increasing WITHIN a marketing version and does not care across
// them. Deriving from the version satisfies both, and unlike a counter it is deterministic: release-
// please regenerates its pull request on every push to main, and a `+1` there would creep upward
// once per unrelated commit.
//
//     0.2.0 -> 20000    0.2.1 -> 20100    0.3.0 -> 30000    1.0.0 -> 10000000
//
// The stride of 100 leaves 99 numbers between two versions, which is where a second build of the
// SAME version goes — `npm pkg set config.buildNumber=20001` by hand. That is why the result is
// max(current, derived) and never just the derived value: a manual bump has to survive the next
// release-please run, and only a monotonic write can promise that.
//
// Android caps versionCode at 2100000000, and any packing into a bounded integer therefore has a
// ceiling somewhere. The digits are spent where this project will actually use them: 209 major
// versions, 999 minor, 99 patch, 99 manual rebuilds. Exceeding one is a hard failure below rather
// than a wrap-around, because the symptom of a number that goes DOWN is a tester's phone refusing
// the install, which nothing in CI would catch.

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const packagePath = fileURLToPath(new URL('../package.json', import.meta.url));
const pkg = JSON.parse(readFileSync(packagePath, 'utf8'));

const version = process.argv[2] || pkg.version;
// Anchored at both ends on purpose. An unanchored match accepts `0.2.0-rc.1` and encodes it to the
// same number as `0.2.0` — two different releases with one versionCode, which Android reads as "not
// newer". release-please never emits a prerelease, so rejecting one costs nothing and closes it.
const match = /^(\d+)\.(\d+)\.(\d+)$/.exec(version ?? '');
if (!match) {
  console.error(`build-number: ${version} is not a plain major.minor.patch version`);
  process.exit(1);
}

const [, major, minor, patch] = match.map(Number);
if (major > 209 || minor > 999 || patch > 99) {
  console.error(
    `build-number: ${version} does not fit the encoding — ` +
      'major must stay under 210, minor under 1000, patch under 100',
  );
  process.exit(1);
}

const derived = major * 10000000 + minor * 10000 + patch * 100;
const current = Number(pkg.config?.buildNumber ?? 0);

process.stdout.write(`${Math.max(derived, Number.isFinite(current) ? current : 0)}\n`);
