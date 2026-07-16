---
title: DNS capability
description: dns.resolve — looking up a hostname's addresses; a read that runs like http.read, but cage-able because DNS is an exfiltration channel.
---

The `dns` family resolves a hostname to its IP addresses.

- **Family:** `dns`
- **Target:** the **hostname** being looked up.
- **Axis:** **read** — a lookup does not change the world, so under the default policy it runs
  **silently, exactly like `http.read` (a GET)**. It does not ask for approval by default.

## Why it is a gated capability anyway

A DNS lookup looks harmless, but it is an **exfiltration channel**: a query to an
attacker-controlled nameserver leaks whatever is encoded into the name (`secret-data.evil.com`).
That is why `dns` is a **first-class capability that goes through the one gateway**, not a free
side-effect the guest can perform directly. The default policy lets reads through, so a plain
lookup runs — but because it passes the same gate as `http`, a **cage or a stricter policy can
scope it per host** (e.g. an agent limited to `dns @ *.internal.example.com`), and a denied host
never leaves the process. The gate is present so you *can* restrict it; the default just doesn't.

## `dns.resolve`

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `host` | string | yes      | The hostname to resolve. |

**Returns** the resolved addresses as a comma-separated string, e.g. `140.82.121.4, 140.82.121.3`.
