---
title: http.write
description: Send data to a URL with a mutating method (POST/PUT/PATCH/DELETE).
---

**Capability:** [`http`](/reference/http/) · <span class="axis axis--write">write</span>

Send data with a mutating method. This **asks for approval** unless a standing grant covers it.
Reach, cage, and credential injection are documented on the [`http` capability](/reference/http/).

## Input

| Field          | Type   | Required | Notes |
|----------------|--------|----------|-------|
| `url`          | string | yes      | The URL to send to. |
| `method`       | string | no       | `POST`, `PUT`, `PATCH`, or `DELETE`. Default `POST`. |
| `body`         | string | no       | The request body. |
| `content_type` | string | no       | `Content-Type` of the body. Default `application/json`. |

## Output

The same `{status, statusText, headers, body}` envelope as [`http.read`](/reference/tools/http-read/).

## From JavaScript

```js
// wrapper (idiomatic):
const r = await fetch("https://api.example.com/messages", {
  method: "POST",
  body: JSON.stringify({ text: "hi" }),
});

// or the generic gate (works for every tool):
const res = JSON.parse(nocturn.call("http.write", {
  url: "https://api.example.com/messages",
  method: "POST",
  body: JSON.stringify({ text: "hi" }),
  content_type: "application/json",
}));
```
