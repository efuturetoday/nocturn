---
title: time.now
description: Get the current date and time. Ungated — carries no authority.
---

**Capability:** — (ungated)

Return the current date and time. A clock read leaks nothing and changes nothing, so it reaches no
capability and is never gated. It exists only because the sandbox guest has no wall clock of its
own — without it a skill could not answer *"what is due today?"*.

## Input

None.

## Output

```json
{ "unix": 1768652645, "iso": "2026-07-17T15:04:05+02:00", "utc": "2026-07-17T13:04:05Z", "timezone": "Europe/Berlin", "offset_seconds": 7200 }
```

## From JavaScript

```js
// wrapper (idiomatic):
const today = nocturn.now().iso;

// or the generic gate:
const t = JSON.parse(nocturn.call("time.now", {}));
```
