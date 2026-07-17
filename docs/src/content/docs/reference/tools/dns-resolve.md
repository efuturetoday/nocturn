---
title: dns.resolve
description: Resolve a DNS record for a hostname.
---

**Capability:** [`dns`](/reference/dns/) · <span class="axis axis--read">read</span>

Resolve a DNS record for a hostname. Runs silently under the default policy. Why a lookup is still
gated (it is an exfiltration channel) and how to cage it lives on the [`dns` capability](/reference/dns/).

## Input

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `host` | string | yes      | The hostname to resolve (an IP for `PTR`). |
| `type` | string | no       | Record type: `A` (IPv4, default), `AAAA` (IPv6), `IP` (both), `MX`, `TXT`, `CNAME`, `NS`, `PTR` (reverse), `SRV`. |

## Output

```json
{ "host": "example.com", "type": "A", "records": ["140.82.121.4", "140.82.121.3"] }
```

`records` are strings: addresses for `A`/`AAAA`/`IP`/`PTR`, `"<pref> <host>"` for `MX`,
`"<priority> <weight> <port> <target>"` for `SRV`, the raw value otherwise.

## From JavaScript

```js
// no wrapper — use the generic gate:
const res = JSON.parse(nocturn.call("dns.resolve", { host: "example.com", type: "A" }));
console.log(res.records);
```
