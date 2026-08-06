---
title: Secrets and accounts
description: Where credentials live, why the model never sees one, and what happens when a secret tries to leave.
---

For the assistant to do anything with a real service it needs a key or a sign-in. Nocturn is built
so it can *use* a credential without ever *seeing* it.

## The vault

Credentials live in an encrypted vault, one per workspace, at
`nocturn-data/workspaces/<name>/vault.enc` (AES-256-GCM). It is unlocked by a master passphrase from
`NOCTURN_MASTER_PASSPHRASE`, stretched with scrypt and then split per workspace: one passphrase
opens every vault, and no two vaults share a key — each is derived for that workspace alone.

Plugins and MCP servers get their own shard, `secrets.enc`, next to their manifest, keyed by the
folder's path. So a credential belongs to the thing that lives in that folder, and renaming or
moving the folder makes its old secrets unreadable.

Without a passphrase Nocturn runs fine — the vaults stay locked, and no credential can be injected.

```sh
printf %s "$TOKEN" | nocturn secret set plugin:my-api/my-api   # a plugin credential
printf %s "$TOKEN" | nocturn secret set mcp:my-server          # an MCP server's bearer
nocturn secret ls                                              # names only, never values
```

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
