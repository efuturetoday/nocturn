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

| Field | Meaning |
|---|---|
| `name` | **Required.** What you store the value under, and what an `oauth` block links to. |
| `host` | **Required.** The only host it is ever attached to. A request anywhere else does not carry it. |
| `header` | **Required.** Which header it is stamped into — usually `Authorization`. |
| `prefix` | Optional, prepended to the value: `"Bearer "` for a bearer token, empty for a bare key. |

Then store the value once:

```sh
printf %s "$TOKEN" | nocturn secret set plugin:my-api/my-api
```

Your code never sees the token — it is stamped in host-side, for that host only.

### Sign in with… instead of a stored token

For a service you log into rather than paste a key from, declare an `oauth` provider. The host runs
the flow, holds the token, refreshes it, and injects it through the credential of the same name:

```json
"credentials": [
  { "name": "gcal", "host": "www.googleapis.com", "header": "Authorization", "prefix": "Bearer " }
],
"oauth": [
  {
    "name": "gcal",
    "auth_url": "https://accounts.google.com/o/oauth2/v2/auth",
    "token_url": "https://oauth2.googleapis.com/token",
    "client_id": "…apps.googleusercontent.com",
    "client_secret": "",
    "scopes": ["https://www.googleapis.com/auth/calendar.readonly"]
  }
]
```

| Field | Meaning |
|---|---|
| `name` | **Required, and must match a `credentials` entry.** That is what the token is injected through — a block with no matching credential fetches a token nothing would ever use, and is refused at load. |
| `auth_url` | **Required, `https`.** Where you are sent to consent. |
| `token_url` | **Required, `https`.** Where the code is exchanged, and where refreshes go. |
| `client_id` | **Required.** The client you registered with that provider. |
| `client_secret` | May be `""`. A desktop-app client's secret is not confidential and is shipped in the manifest; a PKCE client has none at all. |
| `scopes` | **At least one.** |

`oauth` is a **list**, so one plugin can bring several providers — each pairing with its own
credential by name.

Two differences from an [MCP server](/nocturn/guides/remote-mcp/), both worth knowing before you
copy one into the other. A plugin always writes its endpoints out by hand: there is no discovery
mode, so `client_id` and `scopes` live in the manifest rather than arriving as a `-scope` flag at
sign-in time. And the `name` here does double duty as the link to a credential, which an MCP block
has no equivalent of — an MCP server *is* the destination, while a plugin declares which of its
credentials the token becomes.

Then connect it once:

```sh
nocturn auth gcal
```

That prints a consent URL, stores the token in the plugin's own encrypted shard, and refreshes it
from then on. Like a plugin itself, it takes effect at the next start.

**This is a command-line step.** The app's **Settings → Accounts** lists MCP servers in discovery
mode and nothing else, so a plugin's provider does not appear there — connect it from a terminal.
See [Secrets and accounts](/nocturn/guides/connecting-accounts/).

## Putting one in, end to end

The repository ships a runnable one — a `weather` plugin with a single tool, caged to `http_read`:

```sh
cp -r examples/workspace/plugins/weather \
      nocturn-data/workspaces/main/plugins/weather

nocturn serve        # start, or restart if it was already running
```

That is the whole install. Two things follow from it:

**Read `plugin.json` first, and read it instead of `plugin.js`.** The manifest is the complete
statement of what the plugin can reach — `"uses": ["http_read"]` here — and you can check it without
running anything. The code cannot widen it.

**A restart is required.** Plugins are discovered when a workspace opens, and there is no watcher on
`plugins/`. A daemon that was already running does not see the new folder.

:::danger[Installing is the decision — there is no review step]
Dropping the folder in **is** the authorization. Nothing prompts you afterwards, and nothing asks
whether you meant it.

What protects you is structural rather than procedural: the plugin's cage, and the gate on every call
it makes. Both hold whether or not you read the manifest — but they hold it to what the manifest
*says*, and only you can decide whether that is more than you meant to grant. It is short for exactly
that reason. Read it before the folder goes in.
:::

Its tool then appears to the model as `weather_forecast`, and its first request asks about the host
it wants, exactly as if the model had called `http_read` itself.

:::tip[Starting from scratch]
`sdk/_template/` is the skeleton: manifest, JavaScript entry point, and a TypeScript source with a
`tsconfig.json` if you prefer to build.
:::

## When to reach for one

Use a plugin when you would otherwise wish the assistant had a CLI for some service. You get the
same reach, but the token stays with the host, the plugin can only call the base tools it named, and
every actual call still asks you.

Prefer not to write code? A hosted [MCP server](/nocturn/guides/remote-mcp/) gives you tools with no plugin
at all.
