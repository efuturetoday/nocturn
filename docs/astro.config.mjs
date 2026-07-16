// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';

// https://astro.build/config
export default defineConfig({
	site: 'https://nocturn.dev',
	integrations: [
		// Must come BEFORE starlight. Renders ```mermaid blocks client-side,
		// following Starlight's light/dark theme.
		mermaid({ autoTheme: true }),
		starlight({
			title: 'Nocturn',
			description: 'A secure personal AI assistant — mandatory out-of-band approval, WASM isolation, capability broker, in a single Go binary.',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/efuturetoday/nocturn' }],
			// Manual sidebar — explicit sections, order, and nesting.
			// Only list pages that exist; add entries as pages are written.
			sidebar: [
				{
					label: 'Start',
					items: [
						{ label: 'Overview', slug: 'guides/introduction' },
						{ label: 'Install', slug: 'guides/getting-started' },
						{ label: 'Playground', slug: 'guides/the-tui' },
					],
				},
				{
					label: 'Concepts',
					items: [
						{ label: 'Workspace', slug: 'guides/the-workspace' },
						{ label: 'Agents', slug: 'guides/agents' },
						{ label: 'Approvals', slug: 'guides/approvals' },
						{ label: 'Secrets', slug: 'guides/connecting-accounts' },
					],
				},
				{
					label: 'Automation',
					items: [
						{ label: 'Triggers', slug: 'guides/triggers' },
						{ label: 'Channels', slug: 'guides/channels' },
					],
				},
				{
					label: 'Extending',
					items: [
						{ label: 'Plugins', slug: 'guides/writing-plugins' },
						{ label: 'MCP', slug: 'guides/remote-mcp' },
						{ label: 'Skills', slug: 'guides/skills' },
					],
				},
				{
					label: 'Internals',
					items: [
						{ label: 'Threats', slug: 'architecture/threat-model' },
						{ label: 'Design', slug: 'architecture/the-onion' },
						{ label: 'Flow', slug: 'architecture/request-flow' },
						// TODO: Deep dives (nested group): Sandbox, Broker, Approvals, Secrets, Plugins, Decisions
					],
				},
			],
		}),
	],
});
