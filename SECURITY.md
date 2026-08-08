# Security

Nocturn's whole purpose is to be trusted with credentials and personal data, so a security report
here is not a nuisance — it is the most useful thing anyone can send.

## Reporting a vulnerability

**Do not open a public issue.** Use GitHub's private reporting
([Security → Report a vulnerability](https://github.com/efuturetoday/nocturn/security/advisories/new))
so the report stays between us until there is something to update to.

Useful to include, roughly in order of value: which boundary is crossed, the smallest sequence that
crosses it, what an attacker gets, and the version or commit you tested. A proof of concept is
welcome and never required — a precise description of the mechanism is worth more than a script.

Expect an acknowledgement within a few days. This is a personal project, not a staffed product; the
honest promise is that reports are read, taken seriously, and answered, not that a fix ships in
24 hours.

## What is in the threat model

The design assumes both of these are actively hostile, and defends against each differently. The
long version is [the threat model](https://efuturetoday.github.io/nocturn/architecture/threat-model/).

- **Foreign code you installed** — a plugin, a skill, a compiled artifact. It runs in a WASM sandbox
  at zero authority, and every capability is an explicitly handed host window.
- **Prompt injection through content the model reads** — a web page, an email, a tool result. The
  model holds no credential to leak, every effect goes through the gate, and everything that reaches
  the network or the filesystem waits for an approval answered **on a different device**.

Reports about these are exactly on target. So are:

- a sandboxed guest reaching authority it was not handed;
- a secret becoming visible to the model, to a script, or to a plugin — the value must never leave
  the host, only its presence;
- a credential leaving over a channel the egress scanner should have caught;
- a file tool escaping the workspace mount, or reaching the control plane (`grants.json`,
  `vault.enc`, `PERSONA.md`, `agents/`, `skills/`);
- an approval that can be answered, forged, or replayed by anything other than a paired device —
  including a push notification that carries a decision rather than a wake;
- a device enrolling a class of device it does not itself cover;
- a gate ruling that is wrong in the permissive direction, or a grant that widens beyond what was
  approved.

## What is not

Naming these is not a way to dismiss a report — send it anyway if you think it matters. But the
design does not claim to survive:

- **A compromised host.** Anything running as your user can read the process, and the vault
  passphrase currently comes from the environment. Full-disk encryption and OS-level isolation are
  the layer below this one.
- **A hostile or backdoored model endpoint.** The model is untrusted in the sense that its *output*
  is gated, but the endpoint you point it at sees your conversation. Choose it as deliberately as
  you would a hosting provider.
- **A hostile embedding or transcription provider**, for the same reason and with the same rule.
- **The room.** A microphone has no authenticated input: whoever is audible can speak. Speaker
  recognition chooses context and address, never permission, and a spoken instruction that wants
  authority is still confirmed out of band.
- **Physical access to a paired device.** A phone someone else is holding can approve.
- **Denial of service** against your own server on your own network.

## Known gaps

Stated here rather than discovered later:

- `NOCTURN_MASTER_PASSPHRASE` is read from the environment. An interactive unlock is intended and
  not yet built, so the vault is as protected as the environment holding it.
- Skills and plugins are **not signed**. Ed25519 signing and attenuation are designed and unbuilt;
  today, review the manifest before installing, which the plugin flow is deliberately built to let
  you do without executing the artifact.
- There is no append-only audit sink. Decisions are logged; the log is an ordinary file.
- Spoken sessions and speaker recognition are experimental and change faster than the rest.

## Supported versions

The `main` branch. There is no release train yet, so a fix lands on `main` and in the next tag.
