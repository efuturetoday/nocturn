# The permission model as it actually is

> Read this before touching anything security-shaped — it is easy to assume the stricter version.
> Rules and facts only. The reasons are ADR-4, ADR-10, ADR-12, ADR-17, ADR-18, ADR-19, ADR-20; the
> mechanism is in the package doc comments.

## Two separate questions

- **WHICH** tools an agent has at all = agentkit's `ToolSet`, bound once, statically, via `Select`.
- **WHAT** a tool may DO = `gate`, per action, asked when risky, remembered.

## The policies

`internal/workspace/workspace.go:policy` — the workspace root policy:

| Kind | Ruling |
|---|---|
| `NetKind`, `FileKind` | ask, remembered for the session |
| `mail.SendKind` | ask (in **both** policies — ADR-17) |
| everything else | allow |

It is **not** deny-by-default. Tightening it is a deliberate change, not a bugfix.

`agentPolicy` = `policy` plus `memory.Kind` → ask. The one axis staggered by who is watching: a chat
shows the write in its transcript, an unattended run has no reader, so it asks out of band — and with
no device, denies.

**Agent autonomy** (`internal/agent`): `Strict` (the zero value) gets no approver, so a fresh ask is
denied fail-closed; `Guarded` routes the ask out of band. With no device wired, guarded collapses to
strict.

**A terminal approval has no timeout**, unlike `hitl`'s two minutes; `gate.Check` pauses the turn's
clock around the ask. The option SET is the broker's, in the same order, minted from `gate.Action`
alone — but the broker's code is not reused.

**Grants** are durable per workspace (`grants.json`, written 0600 via a temp file) and implement
`gate.Grants`. Recall: never / session / always.

## The system prompt is live, the identity is not

`resolvePersona` is evaluated ONCE at `Open`. The memory index is folded in per turn via
`agentkit.WithSystemFunc` (`composePrompt`). The block is omitted when memory is empty or the runner's
cage holds no memory tool — a narrowed agent must not be handed the user's notes. Agent runs and
sub-agents get the same treatment.

## Credentials

- A credential belongs to what declared it and dies with it (ADR-20). `extension.Decl` is the one
  shape, `bindExtensions` the one registration, the shard beside the folder the one storage. There is
  no workspace-wide `bindings.json` and no `NOCTURN_SECRET_*`.
- Seeding: `nocturn secret set <extension>[/<credential>]` — one grammar, no kind in it; the
  credential may be left out when the extension declares exactly one. The mailbox answers to the same
  grammar (`mail/imap`, `mail/smtp`) though nothing installs it.
- Revoking: `nocturn secret rm`. Values a human types: `nocturn config <extension> k=v`, typed and
  validated on the way in AND on the way out, because they are substituted into text the model reads
  every turn.
- Removing an extension revokes the remembered `NetKind` grant for its credential's host — skills,
  plugins and MCP servers alike (`Workspace.ForgetNetAccess`, `gate.Grants.Forget`).
- The resolution store is REBUILT from disk on every discovery pass, so `secret rm` reaches the
  running daemon. The injector is written at the END of a pass, after everything that can fail, and is
  cleared by asking the injector what it holds, not the last published snapshot.
- Secrets never enter the guest: presence only, value injected host-side at the boundary. Egress
  carrying a secret is blocked, ingress is redacted.
- Mail passwords never ride the HTTP `secret.Injector` — they are used at the IMAP/SMTP boundary by
  the host (ADR-17). They are **two vault entries, not one JSON blob**, so `scanExact` knows each
  value separately; a blob would register as a single secret and let the bare password through. The
  **username stays out of the vault**: it is the household's own address and a registered secret
  would be redacted everywhere it legitimately appears.
- An outgoing mail body runs through `ScanEgress` — model output to a third party is the shortest
  exfiltration path there is — and the refusal handed back to the model is generic, because the
  specific error would tell it which text is a stored secret. Incoming mail is `RedactIngress`.

## Skills, plugins, the catalog

- A skill may declare a credential, which is where ADR-10's "a skill carries zero authority" has its
  boundary: the binding is `Ambient` (a skill has no runtime, so it rides the model's own
  `http_read`). `library.validSkills` REFUSES a credential-bearing skill from a remote catalog and
  accepts one from a file or loopback — the same `signaturePolicy` split plugins use. `go generate
  ./catalog/` declines to publish one and says so.
- **Text needs TLS, code needs a key** (ADR-19). A catalog plugin entry carries manifest and
  `plugin.js` inline and is refused unless an Ed25519 signature over
  `id·folder·sha256(manifest)·sha256(script)·sha256(skill)·sha256(listing)·serial` verifies against a
  key in `library.signingKeys`. `internal/library/freshness.go` remembers the highest serial accepted
  per plugin (`catalog-serials.json`) and refuses to go back.
- What signing does NOT cover is what the manifest asks for: `uses` (the guest's cage — a toolset
  subset, no static host list), `credentials` (binds a token to a host), `oauth` (names the account).
  That triple is the review surface a client must show; `catalogFrame` pulls it out of the signed
  manifest and `plugin.list` says which are already installed.
- A catalog may be a FILE: `NOCTURN_CATALOG_URL=./my-catalog.json` or `file://…`. The signature rule
  is tied to the SOURCE (`Store.signaturePolicy`): remote requires one, a file or loopback does not. A
  signature that IS present must verify either way.
- The catalog fetch is host egress, like the LLM and embedding endpoints and APNs — no model output
  flows into it, so it passes no gate. Lazy, never at startup.
- `library.install` names an entry, it never carries one. `internal/serve/invariant_test.go` walks the
  declared type so the rule survives a convenience field added later. Sideloading stays a file copy on
  the host.
- A plugin may bundle a SKILL.md, folded in by `foldPluginSkills`. A hand-written skill of the same
  name WINS.
- A plugin's tools are exposed before its account is connected, and the failure names the fix
  (`Plugin.explain`).
- A catalog plugin ships NO OAuth client id. The manifest carries the endpoints; the person supplies
  the client once with `nocturn auth <plugin> --client-id …`, stored beside the token in the plugin's
  shard (`pluginRecord` prefers it). The ENDPOINTS still come from the signed manifest.
- Removing an MCP server revokes the remembered grant for its host.

## Devices and the wire (ADR-18)

- `manage` is a **capability, not a gated action**. Held by `ClassApp`, `ClassWeb` and `ClassTool`,
  never by `ClassAppliance`.
- Device classes are interpreted in ONE function, `serve.capabilitiesOf`, with `serve.classFor`
  deriving which class a holder gets from the platform it already sends. `internal/auth` stores a
  class and never compares one. The class is **never on the wire**. `capabilities` is unexported and
  `internal/serve/invariant_test.go` walks the tree for class constants outside three allowlisted
  files.
- `ClassWeb` = approve + enrol, **not** captureAudio — what keeps it from being a second spelling of
  `ClassApp`, and what makes `covers()` refuse to let a browser mint a `ClassTool`.
- `bootstrap` (arm a pairing code) belongs to `ClassTool` alone: its bearer is a 0600 file beside the
  vault.
- Every credential with a TTL has a way to get another one: `nocturn pair` (→ `POST /pair/code`) any
  time, `auth.BootstrapMaxTries = 5`, and `daemon.json` carries **two** bits — `paired` and
  `bootstrap`. `householdCanEnrol` is the single predicate both `serveOn` and `handleDaemon` ask.
- Devices are revocable: `device.list` / `device.forget`, gated on `enrol`, `auth.Store.Forget`.
- Only our own pages may talk to the daemon from a browser (`internal/serve/origin.go`, applied in
  `cors`, which wraps the whole mux and therefore the `/ws` upgrade). Allowed: no `Origin` at all,
  same-origin against `r.Host`, Capacitor's webview origins (read out of the pinned source), and
  `NOCTURN_DEV_ORIGINS`.
- **`hostOK` runs BEFORE `originOK`, and that order is load-bearing.** The Host must be
  un-rebindable: an IP literal, `localhost`, or `.local`. A real hostname is named once via
  `NOCTURN_HOSTNAMES`.
- A browser is not a second device for the unattended case: no push provider carries one, so
  `ClassWeb` answers only while its tab is open.

## Zero ambient authority

The wazero guest gets nothing; every capability is an explicitly handed host window — unforgeable by
absence.
