---
title: HTTP capability
description: http.read and http.write — reaching the network, gated per host, with host-injected credentials.
---

The `http` family lets the assistant reach the network. It is split into two tools so the
**tool the caller picks already fixes the effect axis**: `http.read` is a read, `http.write`
is a write. The security layer gates on that, never on the raw HTTP verb.

- **Family:** `http`
- **Target:** the destination **hostname** (e.g. `api.github.com`). Cages and grants scope on
  it — `http.write @ api.github.com` covers writing to that host, not to any other.
- **Credentials:** attached **host-side, at the boundary, for the matching host only.** The
  guest never sees the token and never chooses it — the destination does. A request that tries
  to carry its own credential (userinfo in the URL, or an `Authorization`/`Cookie`/`X-Api-Key`
  header) is rejected outright.

## `http.read`

Read a URL with a safe method. Runs silently under the default policy (a read).

| Field    | Type   | Required | Notes |
|----------|--------|----------|-------|
| `url`    | string | yes      | The URL to read. |
| `method` | string | no       | `GET` or `HEAD`. Default `GET`. A mutating method here is rejected. |

**Returns** a JSON envelope so the caller sees the real outcome, not just the body:

```json
{ "status": 200, "statusText": "OK", "headers": { "Content-Type": "application/json" }, "body": "…" }
```

## `http.write`

Send data with a mutating method. This is a write, so it **asks for approval** unless a
standing grant already covers it.

| Field          | Type   | Required | Notes |
|----------------|--------|----------|-------|
| `url`          | string | yes      | The URL to send to. |
| `method`       | string | no       | `POST`, `PUT`, `PATCH`, or `DELETE`. Default `POST`. |
| `body`         | string | no       | The request body. |
| `content_type` | string | no       | `Content-Type` of the body. Default `application/json`. |

**Returns** the same `{status, statusText, headers, body}` envelope as `http.read`.

## What the guest never sees

- **Credential headers** are stripped from the response (`Set-Cookie`, `WWW-Authenticate`,
  `Proxy-Authenticate`, …) — the guest has no cookie jar and no business hoarding credential
  material.
- **Leak scanning is bidirectional.** The outbound request (URL + headers + body) is scanned
  before the host's own credential is stamped in; the response body and headers are scanned on
  the way back and any echoed secret is redacted before the model sees it.
- The body is capped at 10 MiB in memory.
