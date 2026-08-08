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
//     0.2.0 -> 20000    0.2.1 -> 20100    0.3.0 -> 30000    1.0.0 -> 1000000
//
// The stride of 100 leaves 99 numbers between two versions, which is where a second build of the
// SAME version goes — `npm pkg set config.buildNumber=20001` by hand. That is why the result is
// max(current, derived) and never just the derived value: a manual bump has to survive the next
// release-please run, and only a monotonic write can promise that.
//
// Android caps versionCode at 2100000000, so this holds to major version 2100.

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const packagePath = fileURLToPath(new URL('../package.json', import.meta.url));
const pkg = JSON.parse(readFileSync(packagePath, 'utf8'));

const version = process.argv[2] || pkg.version;
const match = /^(\d+)\.(\d+)\.(\d+)/.exec(version ?? '');
if (!match) {
  console.error(`build-number: ${version} is not a semantic version`);
  process.exit(1);
}

const [, major, minor, patch] = match.map(Number);
if (minor > 99 || patch > 99) {
  // Not a real limit of anything, but a silent overflow here would hand Android a versionCode that
  // goes DOWN, and the symptom is a failed install on a tester's phone rather than a failed build.
  console.error(`build-number: ${version} overflows the stride — minor and patch must stay under 100`);
  process.exit(1);
}

const derived = major * 1000000 + minor * 10000 + patch * 100;
const current = Number(pkg.config?.buildNumber ?? 0);

process.stdout.write(`${Math.max(derived, Number.isFinite(current) ? current : 0)}\n`);
