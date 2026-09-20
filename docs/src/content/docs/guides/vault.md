---
title: The vault
description: Where every credential lives, how it is stored, why the model never sees one, and what happens when a secret tries to leave.
---

For the assistant to do anything with a real service it needs a key or a sign-in. Nocturn is built
so it can *use* a credential without ever *seeing* it.

**This page is the whole story on credentials.** Plugins, MCP servers and the workspace layout all
touch the vault, and each of those pages links here rather than explaining it again — one place to
correct when it changes.

## Where a credential lives

Not in one file. Every credential sits next to the thing it belongs to, and only the files below
ever hold one:

```
nocturn-data/
├─ master.salt                        ← NOT a secret, but the vault is dead without it
└─ workspaces/
   └─ main/
      ├─ vault.enc                    ← this workspace's own credentials
      ├─ mail/                        ← the mailbox, if one is configured
      │  ├─ mail.json                 (the account — never the password)
      │  └─ secrets.enc               ← nocturn mail setup
      └─ extensions/                  ← everything installed, one folder each
         ├─ home-assistant/
         │  ├─ SKILL.md               (the text — {{config.base_url}} filled in from below)
         │  ├─ manifest.json          (the declaration — names the credential, never holds it)
         │  ├─ config.json            ← nocturn config home-assistant base_url=…
         │  └─ secrets.enc            ← nocturn secret set home-assistant/token
         ├─ my-api/
         │  ├─ plugin.json            (the manifest — names the credential, never holds it)
         │  ├─ plugin.js              (the code the sandbox runs)
         │  └─ secrets.enc            ← nocturn secret set my-api/<credential>
         ├─ weather/                  (no secrets.enc — this one needs no credential)
         │  └─ SKILL.md
         └─ cloudflare/
            ├─ mcp.json               (the declaration — never holds a token)
            └─ secrets.enc            ← nocturn secret set cloudflare, or nocturn auth
```

**Every installed thing has the same shape, and lives in one tree.** A skill, a plugin and an MCP
server are all *extensions*: a folder, a declaration of what it needs, the values a person supplied,
and its own shard. (The mailbox has a folder of its own, `mail/`, for the same credential reason —
but it is not in that tree, because nothing about it is installed.) What a folder CARRIES is read off the files in it — so an integration
that brings a server and the instructions for using it is ONE thing, with one owner and one
credential, rather than three that happen to share a name. The declaration says which credential and which host; the config only ever fills in values it
declared — so whoever edits the config can point the extension at their own server, and can never
point somebody else's credential anywhere.

**One passphrase, many keys.** `NOCTURN_MASTER_PASSPHRASE` is stretched with scrypt over
`master.salt` into a master key, and every file above gets its own key derived from that — per
workspace for `vault.enc`, per folder path for each `secrets.enc`. So one passphrase opens
everything, and no two of these files share a key.

**A shard is bound to where it sits.** Its key comes from the folder's path, and the path is also
the AES-GCM associated data — so `extensions/my-api/secrets.enc` decrypts as `extensions/my-api` and
nothing else. Copy it into another extension's folder and it is unreadable there; rename the folder and
its old secrets are gone. That is not a check that could be skipped, it is the decryption failing.

A shard that will not open is **skipped with a warning**, and the workspace vault is never read as a
substitute. That item simply has no credentials, and the rest of the workspace starts normally.

Without a passphrase Nocturn runs fine — everything stays locked, and no credential can be injected.

```sh
nocturn config home-assistant                            # what does it ask for?
nocturn config home-assistant base_url=https://hass.example.com

printf %s "$TOKEN" | nocturn secret set home-assistant/token
printf %s "$TOKEN" | nocturn secret set my-api/my-api    # a plugin credential
printf %s "$TOKEN" | nocturn secret set my-server        # a server's bearer (its only credential)
nocturn secret ls                                        # names only, never values
nocturn secret rm home-assistant/token                   # and back out again
```

A credential is stored under `ext:<name>@<host>/<credential>` — the host it was issued for is part of
its name. Point the extension at a different address and the key changes with it, so a token
issued for one server is never sent to another.

The value comes from stdin, so it never reaches your shell history or the process list.

## The model never sees the secret

This is the part that matters. The assistant does not receive a key and is not trusted to use it
carefully. Instead:

1. it makes the request with **no credential in it**;
2. at the boundary, on the host side, the credential is stamped in — an `Authorization` header, say;
3. the service sees an authenticated request.

All the guest can ever learn is that a credential *exists*, never its value. There is nothing in its
hands to steal, so a hijacked conversation has nothing to leak.

Credentials are attached only for the destination they belong to. A key bound to `api.example.com`
is never added to a request going anywhere else, so tricking the assistant into calling an
attacker's host does not carry your key along with it.

A request cannot bring its **own** credential, and the two ways it might are closed differently.
An `Authorization` or `Cookie` header is not refused — it is **impossible**: the tools accept a URL,
a method, a body and a content type, and no other header ever reaches the wire. A URL carrying
`user:pass@host` *is* refused, on the way out and on any redirect, because the approval you are
shown is rendered from the host — and a host does not say who is authenticating as what.

The credential channel belongs to the host, and there is exactly one of it.

## Signing in with OAuth

For "sign in with…" services, the host runs the flow once:

```sh
nocturn auth <provider>
```

It prints the provider's consent URL for you to open, catches the redirect on a loopback listener,
and puts the resulting token in the vault. From then on the host refreshes it. The guest never touches it — an
OAuth token is a credential like any other, injected at the boundary.

A plugin can declare its own provider, so connecting a new service is part of installing its plugin.
See [Plugins](/nocturn/guides/writing-plugins/).

## The leak scanner

Never handing over a secret is the first line. The second is watching the boundary in both
directions:

- **Egress.** Before the host attaches its own credential, the outgoing URL, headers and body are
  scanned. A request carrying a stored secret is **blocked**. This catches the case where a secret
  reached the model some other way — pasted into a chat, read out of a file — and is being sent
  somewhere it should not go.
- **Ingress.** Response bodies and header values are scanned and any echoed secret is **redacted**
  before the model sees it, so a service reflecting your key back does not put it into the context.
- **Stripped outright.** Credential-bearing response headers — `Set-Cookie`, `Set-Cookie2`,
  `Authorization`, `WWW-Authenticate`, `Proxy-Authenticate`. The guest has no cookie jar, and it is
  not getting one by accident.

Notifications and reminders take the same egress scan. An out-of-band message is not a side door
around it.

:::note[What the scanner is and is not]
It matches the values of secrets the vault actually holds. It is a backstop for a secret that
escaped its proper channel — not a general-purpose detector of anything secret-shaped, and not a
reason to relax anywhere else.
:::
