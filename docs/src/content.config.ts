import { defineCollection, z } from 'astro:content';
import { docsLoader } from '@astrojs/starlight/loaders';
import { docsSchema } from '@astrojs/starlight/schema';
import { glob } from 'astro/loaders';

// POC: Tier-2 data-driven tool pages (see /tools-demo/<name>/).
// Additive collection alongside `docs` — the real Markdown tool pages under
// src/content/docs/reference/tools/ are untouched. The schema encodes the
// CONVENTIONS.md §6 "two-form" rule: `js.gate` is required, `js.wrapper` is
// nullable so a wrapper-less tool must be declared consciously (null), never
// forgotten.
const tools = defineCollection({
	loader: glob({ pattern: '**/*.yaml', base: './src/data/tools' }),
	schema: z.object({
		title: z.string(),
		description: z.string(),
		capability: z
			.object({
				family: z.string(),
				href: z.string(),
			})
			.nullable(),
		axis: z.enum(['read', 'write', 'none']),
		intro: z.string(),
		input: z
			.array(
				z.object({
					field: z.string(),
					type: z.string(),
					required: z.boolean(),
					notes: z.string(),
				})
			)
			.or(z.literal('none')),
		// Output is EITHER prose (a string, inline-markdown) OR a structured block
		// with optional prose `text` and an optional fenced `code` box (e.g. a JSON
		// envelope) rendered as a real syntax-highlighted code block.
		output: z.union([
			z.string(),
			z.object({
				text: z.string().optional(), // prose lead-in, before the code block
				code: z.string().optional(),
				lang: z.string().default('json'),
				after: z.string().optional(), // prose after the code block (e.g. field notes)
			}),
		]),
		js: z.object({
			// wrapper (idiomatic prelude form) — nullable so a wrapper-less tool
			// is representable, but the author must set it to null on purpose.
			wrapper: z.string().nullable(),
			// generic gate (nocturn.call form) — REQUIRED. This is what makes the
			// two-form rule impossible to drift: no page can ship without it.
			gate: z.string(),
		}),
		notes: z.string().optional(),
	}),
});

// POC: Tier-2 data-driven capability pages (see /caps-demo/<name>/).
// Additive collection alongside `docs` and `tools` — the real Markdown
// capability pages under src/content/docs/reference/*.md are untouched. The
// schema encodes the CONVENTIONS.md §3 canonical section order: the fixed
// spine (glance → tools → cage → limits → optional credentials/leakScanning)
// plus a freeform `sections` tail for capability-specific detail (Confinement,
// Requirements, "Why …") whose bodies are BLOCK markdown.
const capabilities = defineCollection({
	loader: glob({ pattern: '**/*.yaml', base: './src/data/capabilities' }),
	schema: z.object({
		title: z.string(),
		description: z.string(),
		family: z.string(),
		intro: z.string(), // inline markdown
		glance: z.object({
			target: z.string(), // inline markdown
			defaultPolicy: z.string(), // inline markdown
		}),
		tools: z.array(
			z.object({
				name: z.string(),
				href: z.string(),
				axis: z.enum(['read', 'write']),
				desc: z.string().optional(), // inline markdown
			})
		),
		cage: z.object({
			intro: z.string(), // block markdown prose
			examples: z.string(), // a fenced code block body, rendered via <Code>
			lang: z.string().default('json'),
			notes: z.array(z.string()).default([]), // inline-markdown bullets
		}),
		limits: z.array(z.string()).min(1), // inline-markdown bullets — required
		credentials: z.array(z.string()).optional(), // present only if secrets are injected
		leakScanning: z
			.object({
				intro: z.string().optional(),
				points: z.array(z.string()),
			})
			.optional(),
		// Freeform capability-specific tail (Confinement / Requirements / Why …).
		// `body` is BLOCK markdown (paragraphs + lists).
		sections: z
			.array(
				z.object({
					title: z.string(),
					body: z.string(),
				})
			)
			.optional(),
	}),
});

export const collections = {
	docs: defineCollection({ loader: docsLoader(), schema: docsSchema() }),
	tools,
	capabilities,
};
