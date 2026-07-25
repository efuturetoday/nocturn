---
title: Adding capabilities with plugins
description: Give your assistant new powers, like calling an API, with a small locked-down plugin.
---

Out of the box, Nocturn can browse, work with files, and run small scripts. To connect it
to *your* services, such as an API, a web app, or email, you add a plugin. A plugin teaches
the assistant a new action. It runs locked in a sandbox and still has to ask before it does
anything, which makes it safer than handing the assistant a real command-line tool.

This guide builds a tiny plugin so you can see the shape of one.

## The idea

A plugin is a folder inside your workspace:

```
workspaces/default/plugins/my-api/
  plugin.json   ← what it can do, and how far it may reach
  plugin.js     ← the code
```

`plugin.json` declares the plugin. It is what Nocturn shows you when you install it, so you
can see exactly what you are allowing. `plugin.js` is the actual code.

## Declare it

Here is a plugin that adds one action, `send`, which posts a message to an API:

```json
{
  "name": "my-api",
  "version": "0.1.0",
  "tools": [
    {
      "name": "send",
      "description": "Send a message to the API",
      "parameters": { "type": "object", "properties": { "msg": { "type": "string" } } },
      "intent": "Send \"{msg}\" to the API"
    }
  ],
  "cage": [
    { "family": "http", "target": "api.example.com", "access": ["write"] }
  ]
}
```

Three things to notice:

- **`tools`** is what the assistant sees. Here it is one action, `send`, taking a `msg`.
- **`intent`** is the friendly line you see in the approval prompt, *Send "hi" to the API*,
  instead of a raw web address.
- **`cage`** is the hard limit on where the plugin may reach. This one may reach only
  `api.example.com`, and only to write. Anything else, whether another host or a read it
  should not do, is refused outright before you are even asked. The cage sets the maximum.
  You still approve each actual send.

## Write it

`plugin.js` exposes your actions and reaches the outside world through one helper,
`nocturn.call`:

```js
globalThis.plugin.tools = {
  send(args) {
    const res = nocturn.call('http.write', {
      url: 'https://api.example.com/messages',
      method: 'POST',
      body: JSON.stringify({ text: args.msg }),
    });
    return res.body;
  },
};
```

Every request you make this way goes through the same gate as everything else. If it is
inside the cage, Nocturn asks you to approve it. If it is outside, it is blocked and your
code gets an error. Your plugin cannot slip past its cage.

## Connecting to something that needs a login

Many services need an API key or a sign-in. You never put secrets in the plugin. Instead you
declare a credential, and Nocturn holds the secret and attaches it at the last moment. Your
code never sees it. For sign-in flows that use OAuth, the plugin can declare its provider,
and Nocturn walks you through authorizing it once at install time. See
[Secrets and accounts](/guides/connecting-accounts/) for the details.

## Install it

Plugins are picked up when Nocturn starts. The first time, and any time a plugin changes,
Nocturn shows you a review: what it can do, how far it can reach, and what it needs to sign
in to. Nothing is installed until you say yes. Approve it once and it installs quietly after
that, until it changes, when you are asked again with the differences shown.

:::tip[A working example]
The Nocturn repository ships a starting point under `sdk/_template/` — a manifest, the
JavaScript entry point, and its TypeScript source.
:::

## What plugins are good for

Reach for a plugin when you would otherwise wish the assistant had a CLI for some service,
like GitHub, your calendar, or a company API. You get the same reach, but the token stays
with Nocturn, the plugin can only touch what its cage allows, and every change asks first.

Prefer connecting to a remote service rather than running a local tool? Nocturn can also
talk to hosted [MCP servers](/guides/remote-mcp/), with the same safety and no code to
write.
