---
title: ICMP capability
description: the icmp family (tool ping) — an ICMP reachability probe; a read that runs like dns.resolve, but cage-able because the destination host is an exfiltration channel.
---

The `icmp` family sends one ICMP echo to a host to check whether it is reachable and how fast it
answers. The protocol is the authority the broker gates (raw ICMP to a host); the action is
exposed as the **`ping`** tool. Like `dns`, it is a read-only sibling of `http` sharing the same
one gateway.

## At a glance

|                 |                                                        |
|-----------------|--------------------------------------------------------|
| **Family**      | `icmp`                                                 |
| **Target**      | a **hostname or IP** — e.g. `example.com`              |
| **Tools**       | `ping` <span class="axis axis--read">read</span> |
| **Default policy** | runs silently, exactly like `dns.resolve`           |

## Tools

One tool exercises this capability — its page has the inputs, output, and a JavaScript example:

- [`ping`](/reference/tools/ping/) <span class="axis axis--read">read</span> — probe a host with a single ICMP echo

## Cage syntax

A cage bounds where this capability may reach. For `icmp` the `target` is a hostname or IP (a glob
is allowed); `access` is always `["read"]` because a probe never writes. See the
[capabilities overview](/reference/capabilities/#what-a-target-looks-like-it-is-per-family)
for the shared `(family, target, access)` rules.

```json
{ "family": "icmp", "target": "*.internal.example.com", "access": ["read"] }
{ "family": "icmp", "target": "example.com",            "access": ["read"] }
{ "family": "icmp", "target": "192.168.0.0/16",         "access": ["read"] }
```

- A **CIDR range** (`192.168.0.0/16`, `2001:db8::/32`) confines the probe to a subnet — handy
  since a ping is often pointed straight at an IP. A raw IP is pinged directly (no DNS).

## Limits

- **Rate limit** — _TBD_. Enforced **per capability family, not per tool** once wired; the
  sliding-window rate limiter exists as a primitive but is not yet attached to the Guard, so no
  per-family call cap is enforced today.

## Why it is a gated capability anyway

A ping looks harmless, but the **destination host is an exfiltration channel** exactly as a DNS
name is: a probe to an attacker-controlled address (or one whose name encodes data) leaks that
the assistant is active and what it was told to reach. That is why `icmp` is a **first-class
capability that goes through the one gateway**, not a free side-effect. The default policy lets
reads through, so a plain probe runs — but because it passes the same gate as `http`, a **cage or
a stricter policy can scope it per host**, and a denied host never leaves the process.

## Requirements

The probe uses an **unprivileged ICMP socket** (a UDP-based ICMP socket), which needs no root on
macOS and on Linux where `net.ipv4.ping_group_range` permits it. Where the OS forbids it, `ping`
returns a clear error rather than failing silently.
