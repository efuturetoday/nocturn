// Generates the Catalog section of the docs from catalog/ — the same tree the published
// catalog.json is built from.
//
// Written rather than hand-authored because the two would otherwise disagree the first time an entry
// changed, and the one that rots is always the prose. Everything on these pages is read from the
// source of the thing it describes: a plugin's tools and cage come out of its manifest, a skill's
// description out of its frontmatter, and a plugin may bring a GUIDE.md, which becomes the body of
// its page.
//
// Output goes to src/content/docs/catalog/ and is gitignored: it is derived, and committing it would
// invite editing the copy instead of the source.

import { mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

const SOURCE = new URL('../../catalog/', import.meta.url).pathname;
const OUT = new URL('../src/content/docs/catalog/', import.meta.url).pathname;

const read = async (path) => readFile(path, 'utf8');
const readJSON = async (path) => JSON.parse(await read(path));

/** Directories under dir, skipping dot- and underscore-prefixed ones. */
async function dirs(dir) {
	if (!existsSync(dir)) return [];
	const entries = await readdir(dir, { withFileTypes: true });
	return entries.filter((e) => e.isDirectory() && !/^[._]/.test(e.name)).map((e) => e.name);
}

/** The `key: value` pairs of a SKILL.md frontmatter block. */
function frontmatter(body) {
	const match = /^---\n([\s\S]*?)\n---/.exec(body);
	if (!match) return {};
	const out = {};
	for (const line of match[1].split('\n')) {
		const at = line.indexOf(':');
		if (at > 0) out[line.slice(0, at).trim()] = line.slice(at + 1).trim();
	}
	return out;
}

/** YAML-safe single-quoted scalar. */
const quote = (s) => `'${String(s).replaceAll("'", "''")}'`;

async function collect() {
	const skills = [];
	for (const name of await dirs(join(SOURCE, 'skills'))) {
		const body = await read(join(SOURCE, 'skills', name, 'SKILL.md'));
		const entry = await readJSON(join(SOURCE, 'skills', name, 'entry.json'));
		skills.push({ id: name, ...entry, description: entry.description ?? frontmatter(body).description, body });
	}

	const servers = [];
	const mcpDir = join(SOURCE, 'mcp');
	if (existsSync(mcpDir)) {
		for (const file of await readdir(mcpDir)) {
			if (!file.endsWith('.json') || /^[._]/.test(file)) continue;
			servers.push({ id: file.replace(/\.json$/, ''), ...(await readJSON(join(mcpDir, file))) });
		}
	}

	const plugins = [];
	for (const name of await dirs(join(SOURCE, 'plugins'))) {
		const dir = join(SOURCE, 'plugins', name);
		const entry = await readJSON(join(dir, 'entry.json'));
		const manifest = await readJSON(join(dir, 'plugin.json'));
		const guidePath = join(dir, 'GUIDE.md');
		const skillPath = join(dir, 'SKILL.md');
		plugins.push({
			id: name,
			...entry,
			manifest,
			guide: existsSync(guidePath) ? await read(guidePath) : '',
			skill: existsSync(skillPath) ? frontmatter(await read(skillPath)) : null,
		});
	}

	skills.sort((a, b) => a.id.localeCompare(b.id));
	servers.sort((a, b) => a.id.localeCompare(b.id));
	plugins.sort((a, b) => a.id.localeCompare(b.id));
	return { skills, servers, plugins };
}

/** The index: everything the catalog offers, by kind. */
function indexPage({ skills, servers, plugins }) {
	const rows = (items, link) =>
		items
			.map((it) => {
				const name = link ? `[${it.title}](${link}${it.id}/)` : it.title;
				const home = it.homepage ? ` · [site](${it.homepage})` : '';
				return `| ${name} | ${it.description}${home} |`;
			})
			.join('\n');

	return `---
title: The Catalog
description: What a fresh daemon offers in its library — skills, signed plugins and MCP servers, installable with one tap.
---

Every daemon has a library, and out of the box it is this list. Nothing is fetched until somebody
opens it; the daemon reads it from one host over TLS, with every skill body and plugin artifact
**inline**, so installing never fetches from a second place. Point \`NOCTURN_CATALOG_URL\` somewhere
else for your own catalog, or at \`off\` for none — see [the command line](/nocturn/reference/cli/).

## Plugins

Code, sandboxed. A plugin brings typed tools and often a skill that says when to reach for them. Every
plugin entry is **signed** with a key compiled into the daemon: a compromised catalog host can offer
text nobody vouched for, never code. Installing one grants exactly what its manifest asks, shown on
its page below, and in the app before you tap: the tools it adds, the base tools its guest may call,
the hosts a credential would ride to, and the scopes a sign-in would ask for.

| Plugin | What it is |
|---|---|
${rows(plugins, '/nocturn/catalog/')}

## Skills

Instructions, no authority at all ([ADR-10](/nocturn/architecture/threat-model/)) — a skill can tell
the assistant how to work, and everything it then does still meets the gate.

| Skill | What it does |
|---|---|
${rows(skills, null)}

## MCP servers

Remote servers over HTTPS, declared and never carrying a credential. Signing in happens afterwards,
from the app or the command line, and the first call still asks about the host.

| Server | What it is |
|---|---|
${rows(servers, null)}
`;
}

/** One page per plugin: its guide, if it brought one, and what installing it grants either way. */
function pluginPage(p) {
	const m = p.manifest;
	const tools = (m.tools ?? []).map((t) => `| \`${m.name ?? p.id}_${t.name}\` | ${t.description ?? ''} |`).join('\n');
	const uses = (m.uses ?? []).map((u) => `\`${u}\``).join(', ') || 'nothing — it computes and reaches nowhere';
	const hosts = (m.credentials ?? []).map((c) => `\`${c.host}\``).join(', ');
	const scopes = (m.oauth ?? []).flatMap((o) => o.scopes ?? []);

	const grant = [
		`## What installing it grants`,
		'',
		'| | |',
		'|---|---|',
		`| Tools it exposes | ${(m.tools ?? []).length} |`,
		`| Base tools its guest may call | ${uses} |`,
		hosts ? `| A credential rides to | ${hosts} |` : '',
		scopes.length ? `| Sign-in asks for | ${scopes.map((s) => `\`${s}\``).join(', ')} |` : '',
		p.skill ? `| Bundled skill | \`${p.skill.name}\` — its description joins the prompt catalog |` : '',
		'',
		'Its code runs in the WASM sandbox: no ambient authority, brokered imports only, memory-capped',
		'and deadline-bounded. What the sandbox does not decide is the table above — that is the review',
		'surface, and every effect past it still meets the [gate](/nocturn/guides/approvals/).',
		'',
		'## Tools',
		'',
		'| Tool | What it does |',
		'|---|---|',
		tools,
	]
		.filter((line) => line !== '')
		.join('\n');

	return `---
title: ${quote(p.title)}
description: ${quote(p.description)}
---

${p.guide ? p.guide.trim() + '\n\n' : ''}${grant}
`;
}

const { skills, servers, plugins } = await collect();
await rm(OUT, { recursive: true, force: true });
await mkdir(OUT, { recursive: true });
await writeFile(join(OUT, 'index.md'), indexPage({ skills, servers, plugins }));
for (const p of plugins) {
	await writeFile(join(OUT, `${p.id}.md`), pluginPage(p));
}
console.log(`catalog pages: 1 index, ${plugins.length} plugin page(s) (${skills.length} skills, ${servers.length} servers listed)`);
