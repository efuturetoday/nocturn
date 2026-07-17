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
					label: 'Capabilities',
					items: [
						{ label: 'Overview', slug: 'reference/capabilities' },
						{ label: 'HTTP', slug: 'reference/http' },
						{ label: 'DNS', slug: 'reference/dns' },
						{ label: 'Ping', slug: 'reference/ping' },
						{ label: 'Files', slug: 'reference/files' },
						{ label: 'Notify', slug: 'reference/notify' },
						{ label: 'Reminders', slug: 'reference/reminders' },
						{ label: 'Wake', slug: 'reference/wake' },
					],
				},
				{
					label: 'Sandbox',
					items: [
						{ label: 'WASM data format', slug: 'reference/wasm-abi' },
						{ label: 'JavaScript runtime', slug: 'reference/javascript-runtime' },
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
