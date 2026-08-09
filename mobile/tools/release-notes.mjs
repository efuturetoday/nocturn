#!/usr/bin/env node
// The changelog release-please writes, turned into the plain text TestFlight shows testers as
// "What to Test". Node without dependencies, because mobile/ has Node anyway and this is one file.
//
// Two formats meet here and only one of them is ours. release-please emits Markdown built for a
// GitHub release page: bold scopes, a commit link on every line, a compare link in the heading.
// TestFlight renders none of it — whatsNew is plain text with a 4000 character limit — so a raw
// paste would show testers a wall of `* **tui:** … ([44af0c6](https://…))`.
//
// Everything here fails loudly. A release whose notes silently came out empty is worse than one that
// stops: the build would go to external testers with nothing to test against, and nobody finds out
// until a tester asks.
//
//     node tools/release-notes.mjs [version]     # version defaults to package.json's

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const LIMIT = 4000; // TestFlight's cap on whatsNew.

const changelogPath = fileURLToPath(new URL('../CHANGELOG.md', import.meta.url));
const packagePath = fileURLToPath(new URL('../package.json', import.meta.url));

function die(message) {
  console.error(`release-notes: ${message}`);
  process.exit(1);
}

const version = process.argv[2] || JSON.parse(readFileSync(packagePath, 'utf8')).version;
if (!version) die('no version given and package.json has none');

let changelog;
try {
  changelog = readFileSync(changelogPath, 'utf8');
} catch {
  // Expected exactly once: release-please creates mobile/CHANGELOG.md with the first mobile release
  // that follows mobile-v0.1.0. Before that there is nothing to say and nothing to release.
  die(`no CHANGELOG.md at ${changelogPath}`);
}

// `## [0.2.0](compare-link) (2026-08-07)` normally; `## 0.2.0 (…)` when there is nothing to compare
// against. The version is matched literally — a regex built from it would treat the dots as wildcards.
const lines = changelog.split('\n');
const startsSection = (line) => line.startsWith('## ');
const isWanted = (line) =>
  startsSection(line) && (line.startsWith(`## [${version}]`) || line.startsWith(`## ${version} `));

const start = lines.findIndex(isWanted);
if (start < 0) die(`CHANGELOG.md has no section for ${version}`);

const rest = lines.slice(start + 1);
const end = rest.findIndex(startsSection);
const section = end < 0 ? rest : rest.slice(0, end);

const strip = (text) =>
  text
    .replace(/\s*\(\[[0-9a-f]{6,40}\]\([^)]*\)\)/g, '') // the commit link every entry ends with
    .replace(/,?\s*closes\s+(\[[^\]]*\]\([^)]*\)\s*)+/gi, '') // `, closes [#12](…)`
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1') // any link left over keeps its text
    .replace(/\*\*([^*]+)\*\*/g, '$1') // **scope:** is bold only on GitHub
    .replace(/`([^`]*)`/g, '$1')
    .trimEnd();

const out = [];
for (const line of section) {
  if (line.startsWith('### ')) {
    if (out.length) out.push('');
    out.push(strip(line.slice(4)));
  } else if (/^\s*\* /.test(line)) {
    out.push(strip(line.replace(/^(\s*)\* /, '$1- ')));
  } else if (line.trim() !== '') {
    out.push(strip(line));
  }
}

let notes = out.join('\n').trim();
if (!notes) die(`the ${version} section is empty`);

// Cut on a line boundary rather than mid-word, and say in the notes themselves that they were cut —
// a tester who sees the marker knows to look further, which a silent truncation never tells them.
if (notes.length > LIMIT) {
  const marker = '\n\n(cut — the full changelog is in the release on GitHub)';
  const kept = [];
  let used = marker.length;
  for (const line of notes.split('\n')) {
    const room = LIMIT - used;
    if (line.length + 1 <= room) {
      used += line.length + 1;
      kept.push(line);
      continue;
    }
    // The line does not fit. Cutting INSIDE it beats dropping it: one entry longer than the budget
    // would otherwise reduce the notes to the heading above it and the marker — a tester learning
    // less than if nothing had been truncated at all. Below a line's worth of room it is not worth
    // the ragged fragment.
    if (room > 80) kept.push(line.slice(0, room - 1));
    break;
  }
  notes = kept.join('\n').trimEnd() + marker;
  console.error(`release-notes: ${version} exceeded ${LIMIT} characters and was cut`);
}

process.stdout.write(notes + '\n');
