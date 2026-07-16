---
title: DNS capability
description: dns.resolve — looking up a hostname's addresses; a read that runs like http.read, but cage-able because DNS is an exfiltration channel.
---

The `dns` family resolves a hostname to its IP addresses. It is a read-only sibling of `http`
sharing the same one gateway.

## At a glance

|                 |                                                        |
|-----------------|--------------------------------------------------------|
| **Family**      | `dns`                                                  |
| **Target**      | a **hostname** — e.g. `example.com`                    |
| **Tools**       | `dns.resolve` <span class="axis axis--read">read</span> |
| **Default policy** | runs silently, exactly like `http.read`             |

## Tools

### `dns.resolve` <span class="axis axis--read">read</span>

Resolve a DNS record for a hostname. A lookup does not change the world, so under the default
policy it runs **silently, exactly like `http.read` (a GET)** — it does not ask by default.

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `host` | string | yes      | The hostname to resolve (an IP for `PTR`). |
| `type` | string | no       | Record type: `A` (IPv4, default), `AAAA` (IPv6), `IP` (both), `MX`, `TXT`, `CNAME`, `NS`, `PTR` (reverse), `SRV`. |

**Returns** a JSON object `{ "host": "example.com", "type": "A", "records": ["140.82.121.4", "140.82.121.3"] }`.
The `records` are strings: addresses for `A`/`AAAA`/`IP`/`PTR`, `"<pref> <host>"` for `MX`,
`"<priority> <weight> <port> <target>"` for `SRV`, and the raw value otherwise.

The record **type is not an authority axis** — the reach that matters is the queried name (an
exfiltration channel regardless of record), so it is gated on the host exactly the same for every
type.

## Limiting reach — cage syntax

A cage bounds where this capability may reach. For `dns` the `target` is a hostname (a glob is
allowed); `access` is always `["read"]` because a lookup never writes. See the
[capabilities overview](/reference/capabilities/#what-a-target-looks-like-it-is-per-family)
for the shared `(family, target, access)` rules.

```json
{ "family": "dns", "target": "*.internal.example.com", "access": ["read"] }
{ "family": "dns", "target": "example.com",            "access": ["read"] }
```

- A hostname glob (`*.internal.example.com`) matches subdomains; `*` on its own means **any host**.
- **IP/CIDR ranges don't apply to `dns`.** A `dns.resolve` target is the **name being looked
  up**, not an address — so you cage it by name. (IP and CIDR ranges are for `http` and `ping`,
  whose target is a destination *address*.)
- `access` must be explicit; `["write"]` on a `dns` entry is meaningless — DNS only reads.

## Why it is a gated capability anyway

A DNS lookup looks harmless, but it is an **exfiltration channel**: a query to an
attacker-controlled nameserver leaks whatever is encoded into the name (`secret-data.evil.com`).
That is why `dns` is a **first-class capability that goes through the one gateway**, not a free
side-effect the guest can perform directly. The default policy lets reads through, so a plain
lookup runs — but because it passes the same gate as `http`, a **cage or a stricter policy can
scope it per host** (e.g. an agent limited to `dns @ *.internal.example.com`), and a denied host
never leaves the process. The gate is present so you *can* restrict it; the default just doesn't.
