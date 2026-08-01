---
title: Plugins
description: Teach the assistant a new action with a small sandboxed plugin — declared statically, caged to the base tools it names, gated like everything else.
---

Out of the box the assistant can reach the network, work with workspace files and run small scripts.
To connect it to *your* service, you add a plugin: a folder with a manifest and some JavaScript. The
code runs in the WASM sandbox, and every call it makes leaves through a base tool that gates itself
— so a plugin is meaningfully safer than handing the assistant a shell.

## The shape of one

```
nocturn-data/workspaces/main/plugins/my-api/
  plugin.json   ← what it offers, and which base tools it may call
  plugin.js     ← the code
```

Both are read at startup. A malformed plugin is **skipped with a diagnostic** rather than aborting
anything — its tools and credentials are simply absent, which is the fail-closed direction.

## Declare it

```json
{
  "name": "my-api",
  "version": "1",
  "tools": [
    {
      "name": "send",
      "description": "Send a message to the API",
      "parameters": {
        "type": "object",
        "properties": { "msg": { "type": "string", "description": "The message" } },
        "required": ["msg"]
      }
    }
  ],
  "uses": ["http_write"]
}
```

| Field | Meaning |
|---|---|
| `name` | The plugin's identity. Its tools appear to the model as `my-api_send`. |
| `version` | Required, any string. |
| `tools` | What the model sees: name, description, and a JSON-Schema object for the parameters. |
| `uses` | **The cage** — the base tools this plugin's code may call. `["*"]` admits all; omit it entirely for a pure-compute plugin that reaches nothing. |
| `credentials` | Optional: credentials the host injects on its behalf (name, host, header, prefix). |
| `oauth` | Optional: an OAuth2 provider the host runs for it. |

The manifest is parsed **strictly**: an unknown field is an error, not a warning. That is on purpose
— a typo'd permission field must never be silently ignored.

`uses` is the part worth dwelling on. It is not a filter applied to calls; it is the set of tools
the plugin's guest *has*. A plugin that lists only `http_write` does not have a file tool that gets
denied — it has no file tool. Nothing in the prompt, and nothing in the plugin's own code, can
conjure one.

## Write it

```js
globalThis.plugin = {
  tools: {
    send: async (args) => {
      const r = await fetch("https://api.example.com/messages", {
        method: "POST",
        body: JSON.stringify({ text: args.msg }),
      });
      if (!r.ok) throw new Error("send failed: " + r.status);
      return await r.text();
    },
  },
};
```

`fetch` is the prelude's wrapper over `http_read` / `http_write`, so this call goes through the
ordinary gate: the host is approved by you, per request, exactly as if the model had asked for it.
Outside the cage there is nothing to call; inside it, the gate still applies. Both walls, every
time.

The same prelude gives you `nocturn.fs.*`, `nocturn.ping`, `nocturn.resolve`, `nocturn.notify`,
`nocturn.remind`, `nocturn.now` and a node-ish `require("fs")` shim — each mapping to the base tool
of the same name, and each available only if your `uses` list includes it.

## Credentials

Never put a secret in a plugin. Declare it instead, and the host attaches it at the boundary:

```json
"credentials": [
  { "name": "my-api", "host": "api.example.com", "header": "Authorization", "prefix": "Bearer " }
]
```

Then store the value once:

```sh
printf %s "$TOKEN" | nocturn secret set plugin:my-api/my-api
```

Your code never sees the token — it is stamped in host-side, for that host only. For "sign in
with…" services, declare an `oauth` provider instead and run `nocturn auth <name>` once. See
[Secrets and accounts](/nocturn/guides/connecting-accounts/).

## Installing

Drop the folder in `plugins/` and restart. That is the whole install — there is **no interactive
review step today**, so the safety of installing a plugin rests on the two structural walls (its
cage, and the gate on every call), not on a prompt you get at install time. Read the manifest before
you drop it in; it is short by design, and it is the complete statement of what the plugin can
reach.

:::tip[A working example]
`sdk/_template/` in the repository is a runnable starting point: manifest, JavaScript entry point,
and a TypeScript source if you prefer to build.
:::

## When to reach for one

Use a plugin when you would otherwise wish the assistant had a CLI for some service. You get the
same reach, but the token stays with the host, the plugin can only call the base tools it named, and
every actual call still asks you.

Prefer not to write code? A hosted [MCP server](/nocturn/guides/remote-mcp/) gives you tools with no plugin
at all.
