---
title: Ping capability
description: ping — an ICMP reachability probe; a read that runs like dns.resolve, but cage-able because the destination host is an exfiltration channel.
---

The `ping` family sends one ICMP echo to a host to check whether it is reachable and how fast it
answers. Like `dns`, it is a read-only sibling of `http` sharing the same one gateway.

## At a glance

|                 |                                                        |
|-----------------|--------------------------------------------------------|
| **Family**      | `ping`                                                 |
| **Target**      | a **hostname or IP** — e.g. `example.com`              |
| **Tools**       | `ping` <span class="axis axis--read">read</span> |
| **Default policy** | runs silently, exactly like `dns.resolve`           |

## Tools

One tool exercises this capability — its page has the inputs, output, and a JavaScript example:

- [`ping`](/reference/tools/ping/) <span class="axis axis--read">read</span> — probe a host with a single ICMP echo

## Why it is a gated capability anyway

A ping looks harmless, but the **destination host is an exfiltration channel** exactly as a DNS
name is: a probe to an attacker-controlled address (or one whose name encodes data) leaks that
the assistant is active and what it was told to reach. That is why `ping` is a **first-class
capability that goes through the one gateway**, not a free side-effect. The default policy lets
reads through, so a plain probe runs — but because it passes the same gate as `http`, a **cage or
a stricter policy can scope it per host**, and a denied host never leaves the process.

## Limiting reach — cage syntax

A cage bounds where this capability may reach. For `ping` the `target` is a hostname or IP (a glob
is allowed); `access` is always `["read"]` because a probe never writes. See the
[capabilities overview](/reference/capabilities/#what-a-target-looks-like-it-is-per-family)
for the shared `(family, target, access)` rules.

```json
{ "family": "ping", "target": "*.internal.example.com", "access": ["read"] }
{ "family": "ping", "target": "example.com",            "access": ["read"] }
{ "family": "ping", "target": "192.168.0.0/16",         "access": ["read"] }
```

- A **CIDR range** (`192.168.0.0/16`, `2001:db8::/32`) confines the probe to a subnet — handy
  since `ping` is often pointed straight at an IP. A raw IP is pinged directly (no DNS).

## Requirements

The probe uses an **unprivileged ICMP socket** (a UDP-based ICMP socket), which needs no root on
macOS and on Linux where `net.ipv4.ping_group_range` permits it. Where the OS forbids it, `ping`
returns a clear error rather than failing silently.
