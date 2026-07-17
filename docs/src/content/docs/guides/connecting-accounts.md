---
title: Secrets and accounts
description: How Nocturn holds your sign-ins and API keys, and lets an agent use them without ever seeing them.
---

For an agent to do anything useful with a real service, such as reading your email or
calling an API, it needs a key or a sign-in. Nocturn is built so the agent can *use* those
credentials without ever *seeing* them. This page explains where secrets live and how that
works.

## The vault

Every secret, whether an API key or a sign-in token, lives in an encrypted vault. It is a
single file in your workspace, `secrets.vault` (AES-256-GCM). It is locked with a **master
passphrase** you choose on first run: one passphrase opens every workspace's vault, yet no
two workspaces share a key — each vault's key is derived from the master for that workspace
alone. The passphrase is never written anywhere, and nothing sensitive is ever stored in
the clear.

Because the vault is encrypted, copying your [workspace](/guides/the-workspace/) to another
machine copies the locked vault. Your tokens cannot be read without you there to unlock it.

## The agent never sees the secret

This is the key point. When an agent needs to call a service that requires a key, it does
not get handed the key. Instead:

1. The agent makes the request, say a POST to `api.example.com`, with no credential in it.
2. At the very last moment, as the request leaves your machine, Nocturn attaches the
   credential itself, for example an `Authorization` header.
3. The service sees a properly authenticated request. The agent never saw the token.

All the agent can ever learn is that a credential exists, never its value. So even a
hijacked agent has nothing to steal and nothing to leak. The secret was never in its hands.

Credentials are attached only for the destination they belong to. A key for
`api.example.com` is never added to a request going anywhere else. A prompt injection that
tricks an agent into calling an attacker's server cannot smuggle your key along.

## Signing in with OAuth

For services that use "Sign in with…", like Google, Nocturn runs the sign-in once, at
setup. It opens the provider's consent page, you approve, and the resulting token goes
straight into the vault. From then on Nocturn refreshes it automatically, so you do not sign
in again. As always, the agent never touches the token itself.

Plugins can bring their own sign-in, so connecting a new service is part of installing its
plugin. See [Plugins](/guides/writing-plugins/).

## A safety net for leaks

On top of never handing over secrets, Nocturn watches the data crossing the boundary in
both directions:

- **Going out.** If a secret's value somehow appears in something an agent is about to send,
  the request is blocked. This is a last line of defense against exfiltration.
- **Coming in.** If data an agent fetches contains something that looks like a secret, it is
  redacted before the agent sees it, so credentials do not leak into its context.

Together with attaching secrets host-side, this keeps your secrets yours: held encrypted,
attached only where they belong, and never exposed to the part of the system that reads
untrusted content.
