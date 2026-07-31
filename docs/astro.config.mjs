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
			description: 'A secure personal AI assistant — mandatory out-of-band approval, WASM isolation, a permission gate the engine cannot see around, in a single Go binary.',
			customCss: ['./src/styles/brand.css'],
			// Lightweight scroll parallax for the splash hero. Drives two CSS
			// custom properties (--nebula-shift on .hero, --mascot-shift on the
			// image); no-ops when there's no hero or reduced motion is preferred.
			head: [
				{
					tag: 'script',
					content: `(function(){
	if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
	function init(){
		var hero = document.querySelector('.hero');
		if (!hero) return;
		var img = hero.querySelector('img');
		var ticking = false;
		function update(){
			ticking = false;
			var y = window.scrollY || 0;
			var s = Math.min(y, 700);
			hero.style.setProperty('--nebula-shift', (s * 0.28) + 'px');
			if (img) img.style.setProperty('--mascot-shift', (s * -0.14) + 'px');
		}
		function onScroll(){ if (!ticking){ ticking = true; requestAnimationFrame(update); } }
		addEventListener('scroll', onScroll, { passive: true });
		update();
	}
	if (document.readyState !== 'loading') init();
	else addEventListener('DOMContentLoaded', init);
})();`,
				},
			],
			// Dark only: force the theme and drop the light/dark toggle.
			components: {
				ThemeProvider: './src/components/ThemeProviderDark.astro',
				ThemeSelect: './src/components/ThemeSelectNone.astro',
			},
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/efuturetoday/nocturn' }],
			// Manual sidebar — explicit sections, order, and nesting.
			// Only list pages that exist; add entries as pages are written.
			sidebar: [
				{
					label: 'Start',
					items: [
						{ label: 'Overview', slug: 'guides/introduction' },
						{ label: 'Install', slug: 'guides/getting-started' },
						{ label: 'The Chat', slug: 'guides/the-chat' },
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
						{ label: 'Remote Access', slug: 'guides/remote-access' },
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
					// The gate Kinds that exist in the code — no more, no fewer. The kind entries
					// stay lowercase: they are the literal Kind values, not prose.
					label: 'The Gate',
					items: [
						{ label: 'Cage and Gate', slug: 'reference/gate' },
						{ label: 'net', link: '/reference/gate/net/' },
						{ label: 'file', link: '/reference/gate/file/' },
						{ label: 'notify', link: '/reference/gate/notify/' },
						{ label: 'remind', link: '/reference/gate/remind/' },
						{ label: 'memory', link: '/reference/gate/memory/' },
					],
				},
				{
					label: 'Tools',
					items: [
						{
							label: 'Network',
							items: [
								{ label: 'http_read', link: '/reference/tools/http_read/' },
								{ label: 'http_write', link: '/reference/tools/http_write/' },
								{ label: 'dns_resolve', link: '/reference/tools/dns_resolve/' },
								{ label: 'ping', link: '/reference/tools/ping/' },
							],
						},
						{
							label: 'Files',
							items: [
								{ label: 'file_read', link: '/reference/tools/file_read/' },
								{ label: 'file_list', link: '/reference/tools/file_list/' },
								{ label: 'file_stat', link: '/reference/tools/file_stat/' },
								{ label: 'file_search', link: '/reference/tools/file_search/' },
								{ label: 'file_write', link: '/reference/tools/file_write/' },
								{ label: 'file_remove', link: '/reference/tools/file_remove/' },
								{ label: 'file_move', link: '/reference/tools/file_move/' },
							],
						},
						{
							label: 'Reaching You',
							items: [
								{ label: 'notify', link: '/reference/tools/notify/' },
								{ label: 'remind', link: '/reference/tools/remind/' },
								{ label: 'remind_list', link: '/reference/tools/remind_list/' },
								{ label: 'remind_cancel', link: '/reference/tools/remind_cancel/' },
							],
						},
						{
							// Split across two groups on purpose, because they are not the same kind of
							// thing: writing a note is gated, reading one back is context and carries no
							// authority at all — the same argument that leaves skill_read ungated.
							label: 'Memory',
							items: [
								{ label: 'memory_read', link: '/reference/tools/memory_read/' },
								{ label: 'memory_write', link: '/reference/tools/memory_write/' },
							],
						},
						{
							label: 'Zero Authority',
							items: [
								{ label: 'time_now', link: '/reference/tools/time_now/' },
								{ label: 'whoami', link: '/reference/tools/whoami/' },
								{ label: 'wake', link: '/reference/tools/wake/' },
								{ label: 'code_run', link: '/reference/tools/code_run/' },
								{ label: 'skill_read', link: '/reference/tools/skill_read/' },
							],
						},
					],
				},
				{
					label: 'Sandbox',
					items: [
						{ label: 'WASM Data Format', slug: 'reference/wasm-abi' },
						{ label: 'JavaScript Runtime', slug: 'reference/javascript-runtime' },
					],
				},
				{
					label: 'Internals',
					items: [
						{ label: 'Threats', slug: 'architecture/threat-model' },
						{ label: 'The Two Halves', slug: 'architecture/agentkit' },
						{ label: 'Speaking to It', slug: 'architecture/live-voice' },
						{ label: 'Design', slug: 'architecture/the-onion' },
						{ label: 'Flow', slug: 'architecture/request-flow' },
					],
				},
			],
		}),
	],
});
