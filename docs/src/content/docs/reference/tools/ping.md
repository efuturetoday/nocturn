---
title: ping
description: Probe a host with a single ICMP echo to check reachability.
---

**Capability:** [`icmp`](/reference/icmp/) · <span class="axis axis--read">read</span>

Probe a host with one ICMP echo. Runs silently under the default policy. Why it is gated and how to
cage it (per host / CIDR) lives on the [`icmp` capability](/reference/icmp/).

## Input

| Field  | Type   | Required | Notes |
|--------|--------|----------|-------|
| `host` | string | yes      | The hostname or IP to ping. |

## Output

```json
{ "host": "example.com", "ip": "93.184.216.34", "ok": true, "rtt_ms": 12 }
```

## From JavaScript

```js
// wrapper (idiomatic):
const r = nocturn.ping("example.com");
console.log(r.ok, r.rtt_ms);

// or the generic gate:
const res = JSON.parse(nocturn.call("ping", { host: "example.com" }));
```
