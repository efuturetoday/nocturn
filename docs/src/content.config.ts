import { defineCollection, z } from 'astro:content';
import { docsLoader } from '@astrojs/starlight/loaders';
import { docsSchema } from '@astrojs/starlight/schema';
import { glob } from 'astro/loaders';

// Data-driven tool pages, rendered by src/pages/reference/tools/[...slug].astro.
// One YAML per registered tool; the file name IS the tool name (underscored,
// exactly as the model calls it).
//
// Two invariants the schema enforces, because both have drifted before:
//   `gated` is required — a page cannot ship without saying whether calling the
//   tool can stop for a human. It is NOT derivable from `axis` (file_read is a
//   read and ungated; http_read is a read and still asks).
//   `js.wrapper` is nullable, not optional — a tool without a prelude wrapper
//   must declare null on purpose, so a forgotten wrapper is a type error.
const tools = defineCollection({
	loader: glob({ pattern: '**/*.yaml', base: './src/data/tools' }),
	schema: z.object({
		title: z.string(), // the registered tool name, e.g. http_read
		description: z.string(),
		// The kind page this tool belongs to, or null for a tool that belongs to none
		// (time_now, wake, code_run). Belonging is not the same as being gated: the
		// file read tools belong to `file` and never call the gate — see `gated`.
		kind: z
			.object({
				name: z.string(), // the Kind's value, e.g. "net"
				href: z.string(),
				target: z.string().optional(), // what Target carries when this tool checks
			})
			.nullable(),
		gated: z.boolean(),
		// Descriptive only: does the tool observe, or change something? It does not
		// decide gating — see `gated`.
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
			// generic form (nocturn.call) — REQUIRED, so no page can ship without
			// the form that always works.
			call: z.string(),
		}),
		notes: z.string().optional(),
	}),
});

// Data-driven gate-kind pages, rendered by src/pages/reference/gate/[...slug].astro.
// One YAML per gate Kind that exists in the code — today exactly four:
// tools.NetKind, tools.FileKind, tools.NotifyKind, tools.RemindKind. There is no
// page for a family the gate does not know: dns_resolve and ping gate on `net`,
// so they live there rather than in invented `dns`/`icmp` kinds.
const kinds = defineCollection({
	loader: glob({ pattern: '**/*.yaml', base: './src/data/kinds' }),
	schema: z.object({
		title: z.string(),
		description: z.string(),
		kind: z.string(), // the Kind's value, e.g. "net"
		constant: z.string(), // the Go identifier it comes from, e.g. tools.NetKind
		intro: z.string(), // inline markdown
		glance: z.object({
			target: z.string(), // what Action.Target carries (inline markdown)
			policy: z.string(), // what the workspace root policy rules (inline markdown)
			matcher: z.string(), // how a grant pattern is matched (inline markdown)
		}),
		tools: z.array(
			z.object({
				name: z.string(),
				href: z.string(),
				axis: z.enum(['read', 'write']),
				gated: z.boolean(),
				desc: z.string().optional(), // inline markdown
			})
		),
		// How a remembered grant for this kind is written and what it covers.
		grants: z.object({
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
		// Freeform kind-specific tail (Confinement / Requirements / Why …).
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
	kinds,
};
