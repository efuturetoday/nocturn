The Gmail plugin gives the assistant three tools — `gmail_search`, `gmail_read`, `gmail_labels` — and
nothing else. It is read-only, on purpose (see [Why read-only](#why-read-only)).

Installing it takes a tap. Connecting it takes about ten minutes the first time, because **you supply
your own OAuth client**, and the rest of this page is why that is unavoidable and what to click.

## Install it

Open the library in the app or the browser UI, filter to **Plugins**, pick **Gmail (read-only)**,
install. What you are agreeing to is shown before the button, read out of the manifest: the tools it exposes, the base tools its guest may call (`http_read` and
nothing more), and the host a credential would be attached to (`gmail.googleapis.com`).

Its code runs in the WASM sandbox — no ambient authority, brokered imports only, memory-capped and
deadline-bounded — and every request it makes still meets the [gate](/nocturn/guides/approvals/) like
any other. The plugin also brings a `SKILL.md`, which is why the assistant knows Gmail's query syntax
instead of guessing at it.

Nothing works yet. The plugin has no account.

## Why you register your own OAuth client

`gmail.readonly` is what Google calls a **restricted scope**. An application that ships one OAuth
client for all its users must pass Google's verification *and* an annual third-party security
assessment (CASA) — for Gmail scopes, a full penetration test by an approved lab, repeated every
twelve months. No self-hosted assistant is going to ship that, and we would not want to: every
household's mail would then run through a single Google project, ours.

So the client is yours. It costs nothing, and Google asks nothing of you for using it with your own
account.

## Create it

1. Open the [Google Cloud Console](https://console.cloud.google.com/) and create a project — any
   name; it is yours alone.
2. **APIs & Services → Library →** enable the **Gmail API**.
3. **APIs & Services → OAuth consent screen**: choose **External**, fill in an app name and your own
   address as support and developer contact.
4. **Set the publishing status to "In production".** (Verification and the CASA assessment are what
   an app needs to serve OTHER people; your own client, used by your own account, needs neither —
   what unverified costs you is the warning screen and a cap of 100 users.) This is the step everybody skips and everybody
   regrets: while the consent screen sits in *Testing*, Google revokes the refresh token after **7
   days** and the assistant loses access every week. In production it keeps working. Your own
   unverified app shows a "Google hasn't verified this app" screen the first time — click through it
   under *Advanced*. Verification only matters for handing the app to other people.
5. **Add the scope** `https://www.googleapis.com/auth/gmail.readonly`.
6. **Credentials → Create credentials → OAuth client ID → Desktop app.** Copy the client id and the
   client secret.

## Connect it

On the machine running the daemon, once:

```sh
printf %s 'GOCSPX-xxxxxxxx' |
  nocturn auth gmail -client-id 123456.apps.googleusercontent.com -client-secret-stdin
```

The secret goes in on **stdin**, not as a flag: a flag lands in your shell history and in every `ps`
on the machine. Google calls a desktop client's secret non-confidential, which is a claim about
Google's threat model rather than about yours.

It prints a URL. Open it, pick the account, click through the unverified-app screen. The token **and**
the client land encrypted in that plugin's own folder
(`workspaces/<ws>/plugins/gmail/secrets.enc`) — not in the workspace vault, not in the manifest, and
never in the assistant's context. Re-running `nocturn auth gmail` later needs no flags: the client is
remembered.

The vault must be unlocked for this, so `NOCTURN_MASTER_PASSPHRASE` has to be set — see
[Vault](/nocturn/guides/vault/).

## What happens on the first call

The assistant calls `gmail_search`, and the gate asks you — once — about `gmail.googleapis.com`, on
your phone or in the app. The token is stamped in host-side at the boundary; the plugin's code never
sees it, and neither does the model.

If you skipped the connect step, the tool says so rather than failing obscurely:

```
secret "plugin:gmail/account": secret not found — the gmail account is not
connected yet; run: nocturn auth account
```

## Why read-only

The gate's target on the network axis is a **host**. One approval for `gmail.googleapis.com` would
cover reading and sending alike, and "may send mail" is not something to hand over as a side effect of
"may read mail" — least of all for an assistant that reads text other people wrote, which is the
prompt-injection surface par excellence.

Sending belongs to a tool that can ask about a **recipient**. That is what the mail package is being
built for, and it will cover IMAP accounts generally.

## The alternative: IMAP with an app password

If none of the above appeals, wait for that mail tool. IMAP with an app password sidesteps the whole
apparatus — no Cloud project, no consent screen, no publishing status, no weekly expiry — and the same
credential shape covers iCloud, Fastmail, mailbox.org, a server of your own, and Proton through its
Bridge. Gmail supports app passwords too, once two-factor authentication is on.
