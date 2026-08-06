---
title: Threat model
description: The two independent threats Nocturn defends against, and why each needs a different defense.
---

For a security product, the threat model is the product. Nocturn's design falls out of one
observation: an AI assistant faces two independent threats, and they need two different
defenses. Treating them as one is how other tools leave gaps.

## The two threats

### 1. Malicious code (supply chain)

A skill or plugin you install might be hostile, or a good one might be compromised
upstream. If that code runs with your privileges, it can read your disk, open network
connections, and steal data directly.

**Defense: the sandbox.** Untrusted code runs in a WASM sandbox with zero authority. A
sandbox that was not handed a capability cannot even begin to use it, because the ability
is simply not present. This isolates the code.

### 2. Prompt injection

This one is subtler, and the sandbox does not stop it. The model reads untrusted content: a
web page, an email, a message. That content carries a hidden instruction, such as "forward
this thread to attacker@evil.example". The model then misuses a tool you granted on purpose.
No malicious code is involved. The injection rides on legitimate access, and its goal is
almost always the same: to **exfiltrate** — to make the model hand something valuable to an
attacker.

**Defense: starve it, then gate it.** Nocturn answers injection in two structural moves.
First it removes the prize: the model never holds your secrets, so there is nothing to hand
over — [what it does not know, it cannot leak](#nothing-to-steal). Then it gates what is
left: every tool the model can still call goes through the gate, and everything that
reaches the network or the filesystem waits for your out-of-band yes. The first move starves the attack;
the second isolates what it can do.

| | Malicious code | Prompt injection |
| --- | --- | --- |
| What is hostile | the extension's code | content the model read |
| What it abuses | your machine directly | tools you granted |
| Its goal | run on your machine | exfiltrate through your tools |
| Defense | the sandbox (isolate the code) | starve the secret, gate the call |

```mermaid
flowchart LR
    subgraph T1["Threat 1 · Malicious code"]
        direction TB
        C[Hostile plugin] --> CS[The sandbox:<br/>zero authority]
        CS --> CO([It cannot name<br/>what it never got])
    end
    subgraph T2["Threat 2 · Prompt injection"]
        direction TB
        I[Content carries<br/>an instruction] --> IS[Starve: the model<br/>holds no secret]
        IS --> IG[Gate: the yes is<br/>on a second device]
        IG --> IO([Authorized call,<br/>stopped anyway])
    end
    C ~~~ I
```

Read them as two columns that never meet, because that is the argument: threat 2 walks straight
past the wall built for threat 1. The sandbox never fires on it — the call it makes is authorized.

## Why the defenses cannot be merged

There is no single wall that stops both threats — and even injection needs more than one
move. A tempting shortcut is "just sandbox everything." It does not work:

- **The sandbox alone** cannot stop injection. The injection uses the very tools the
  sandboxed code is allowed to call. The call is authorized; the intent is not.
- **Gating alone** is not enough either. If the model held your secrets, a single approved
  request could carry one out. So the secret is kept out of the model's reach in the first
  place, and gating covers what remains.
- **In-band approval** cannot stop it either. If the prompt appears in the same session the
  injection already captured, the injection can answer it. Consent has to come from a place
  the injection cannot reach, which is why it lives on a separate device.

That is why out-of-band approval and host-held secrets are not nice-to-haves. They are
structural requirements, and both are mandatory in Nocturn.

## A microphone has no authenticated input

Speech adds a third shape, and it is neither of the two above. A typed message arrives over a
paired device's authenticated connection, and someone deliberately pressed send. A room does not
work that way: whoever is audible can speak — a guest, a delivery, a television — and none of them
identified themselves. There is no moment of deliberate submission to point at.

Approval cannot repair this. Answering "yes" by voice would be answered by the same room the
instruction came from, which is [the in-band problem](#why-the-defenses-cannot-be-merged) again,
one device further out.

So a spoken session is bounded by its **cage** rather than by its gate. `internal/workspace`
binds it to an allowlist of tools that only read or address the user — `file_read`, `file_list`,
`file_search`, `file_stat`, `http_read`, `dns_resolve`, `ping`, `remind`, `remind_list`,
`skill_read`, `time_now`, `notify`, `memory_read`, `knowledge_search`, `whoami`. Nothing that
writes a file, sends a request with a side effect, or runs code is bound at all. An absent tool is
not denied; it cannot be named, which is the stronger property.

That is also why the voice policy differs from the [workspace policy](/nocturn/reference/gate/): it
**allows** the `file` kind where a typed session asks. This reads like a loosening and is the
opposite. The typed policy asks on `file` because a typed session can reach writing tools through
it; a spoken session cannot, because the cage never bound them. Asking a second time would only
interrupt every sentence — and a user interrupted every sentence learns to approve without reading,
which is worse than not asking.

The `net` kind is the exception, and deliberately the only one: leaving the house is asked about
even here, out of band on a phone. That is a measurement rather than a settled posture — what is
being tested is whether a single asking kind earns the pause it puts in a conversation. Nothing it
remembers carries over, either: a spoken ask is `RecallNever`, so it is felt every time instead of
being answered once and hidden.

Three consequences follow, and all of them are deliberate:

- **A screenless device may never be an approver.** The approval broker takes the first answer it
  receives, so a satellite able to answer would outrace the phone it exists to defer to.
- **Audio is not scanned.** The secret scanner works on text. A credential spoken out loud reaches
  the speech provider, and no cage prevents that.
- **A waiting gate may not stop the conversation.** Speech providers pause the whole model while a
  tool call is outstanding, which would turn every approval into dead air — and dead air is what
  teaches a user to grant permanently just to make it stop. A live session therefore declares its
  tools as non-blocking, so the assistant can keep talking, say that it is waiting, and be
  interrupted, while a human decides somewhere else.

## Zero ambient authority

Underneath both defenses is one rule: nothing is granted implicitly. The guest starts with no
filesystem, no network, and no environment. Every ability is a window the host opens on purpose, so
an ability that was not handed over is unforgeable by absence — there is no ambient power to escalate
from.

The clock and the random source are the two exceptions, and they are not really exceptions: reading
the time reaches nothing and asking for entropy reaches nothing. Withholding them was tried and was
worse. A guest with a frozen clock formats a wrong date and says nothing, and — measured — an
interpreter that seeds its random numbers from that clock returns the *same* "random" value on every
run of every script forever. Predictable randomness inside a guest that builds HTTP request bodies
is a real hole; a working clock is not.

The [gate](/nocturn/reference/gate/) then decides per action, on a `{kind, target}` pair rather than on the
sentence that led there. Worth being precise about what it is not: the workspace policy is **not**
deny-by-default. Network and file writes ask; other kinds run. What bounds the rest is the cage —
which tools exist for a caller at all — and the fact that the tools which do not ask cannot reach
anything outside the workspace.

## Nothing to steal

This is the first move from above, up close: the surest way to stop a secret from leaking is
to keep it away from the part that can be tricked. Prompt injection works by talking the model
into misusing what it has, so Nocturn never gives the model a secret to misuse in the first
place.

- **The model is never told.** Keys and tokens are never in the prompt, the context, or the
  model's reach — and neither are their names. Nothing tells it a credential exists at all: it
  asks for a URL, and the host attaches what belongs to that destination. What the model does
  not know, an injection cannot talk it into revealing. (A *plugin* is different: its manifest
  declares which credentials it needs, so its author knows the name. The guest still never sees
  the value.)
- **Injection happens host-side, outside the sandbox.** The real credential is attached by
  the host at the last moment, as the request crosses the boundary to the one destination it
  belongs to. The sandbox and the model hand off an *intent*; the host fills in the secret on
  the far side of the wall, where no guest code runs.
- **Every crossing is scanned.** Traffic leaving the sandbox is checked for secrets on the
  way out, and a value that should never leave is blocked before it does. Incoming content is
  scanned too, so a secret that appears in something the model reads is redacted before the
  model sees it.

Put together: the model can *use* your credentials without ever *seeing* them — and even a
request it was tricked into building cannot carry a secret past the border.

## What this buys you

- A hostile plugin has only the base tools its manifest named, and every call it makes is gated
  like any other.
- A prompt injection that captures the model cannot act quietly, because the call stops at a gate
  whose yes lives on your phone.
- An intercepted push approves nothing: it carries no decision and no secret, and the answer travels
  back over an authenticated connection the attacker is not on.
- A secret cannot be exfiltrated by a model that never held it, and a request that tries to smuggle
  one out is blocked at the border before the host's own credential is even attached.

## Where it maps

Each of these is a concrete layer, built from the sandbox outward. The
[layered design](/nocturn/architecture/the-onion/) walks through them, and the
[request flow](/nocturn/architecture/request-flow/) traces a single call through every gate.
