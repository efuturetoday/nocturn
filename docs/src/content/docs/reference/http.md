---
title: HTTP capability
description: http.read and http.write — reaching the network, gated per host, with host-injected credentials.
---

The `http` family lets the assistant reach the network. It is split into two tools so the
**tool the caller picks already fixes the effect axis**: `http.read` is a read, `http.write`
is a write. The security layer gates on that, never on the raw HTTP verb.

## At a glance

|                 |                                                        |
|-----------------|--------------------------------------------------------|
| **Family**      | `http`                                                 |
| **Target**      | a **hostname** — e.g. `api.github.com`                  |
| **Tools**       | `http.read` <span class="axis axis--read">read</span> · `http.write` <span class="axis axis--write">write</span> |
| **Default policy** | reads run silently; writes ask for approval         |

## Tools

### `http.read` <span class="axis axis--read">read</span>

Read a URL with a safe method. Runs silently under the default policy.

| Field    | Type   | Required | Notes |
|----------|--------|----------|-------|
| `url`    | string | yes      | The URL to read. |
| `method` | string | no       | `GET` or `HEAD`. Default `GET`. A mutating method here is rejected. |

**Returns** a JSON envelope so the caller sees the real outcome, not just the body:

```json
{ "status": 200, "statusText": "OK", "headers": { "Content-Type": "application/json" }, "body": "…" }
```

### `http.write` <span class="axis axis--write">write</span>

Send data with a mutating method. This **asks for approval** unless a standing grant covers it.

| Field          | Type   | Required | Notes |
|----------------|--------|----------|-------|
| `url`          | string | yes      | The URL to send to. |
| `method`       | string | no       | `POST`, `PUT`, `PATCH`, or `DELETE`. Default `POST`. |
| `body`         | string | no       | The request body. |
| `content_type` | string | no       | `Content-Type` of the body. Default `application/json`. |

**Returns** the same `{status, statusText, headers, body}` envelope as `http.read`.

## Limiting reach — cage syntax

A cage bounds where this capability may reach. For `http` the `target` is a hostname,
an IP, or a CIDR range (a glob is allowed); `access` is the read/write axis. See the
[capabilities overview](/reference/capabilities/#what-a-target-looks-like-it-is-per-family)
for the shared `(family, target, access)` rules.

```json
{ "family": "http", "target": "api.github.com",        "access": ["read"] }
{ "family": "http", "target": "*.githubusercontent.com", "access": ["read"] }
{ "family": "http", "target": "api.example.com",        "access": ["read", "write"] }
{ "family": "http", "target": "10.0.0.0/8",              "access": ["read"] }
{ "family": "http", "target": "*",                       "access": ["read"] }
```

- A hostname glob (`*.github.com`) matches subdomains; `*` on its own means **any host**.
- A **CIDR range** (`10.0.0.0/8`, `2001:db8::/32`) confines the caller to a subnet — it matches
  a request made **to an IP in that range** (a hostname is matched by a hostname glob, since the
  broker never resolves names).
- `access` must be explicit — a missing `access` is a fail-closed error, never a silent "both".
- The cage only sets the *maximum* reach. Within it, writes still ask each time (until you
  grant them).

## Credentials & leak scanning

- **Credentials are attached host-side, at the boundary, for the matching host only.** The
  guest never sees the token and never chooses it — the destination does. A request that tries
  to carry its own credential (userinfo in the URL, or an `Authorization` / `Cookie` /
  `X-Api-Key` header) is rejected outright.
- **Credential headers are stripped from the response** (`Set-Cookie`, `WWW-Authenticate`,
  `Proxy-Authenticate`, …) — the guest has no cookie jar and no business hoarding credential
  material.
- **Leak scanning is bidirectional.** The outbound request (URL + headers + body) is scanned
  before the host's own credential is stamped in; the response body and headers are scanned on
  the way back and any echoed secret is redacted before the model sees it.
- The response body is capped at 10 MiB in memory.
