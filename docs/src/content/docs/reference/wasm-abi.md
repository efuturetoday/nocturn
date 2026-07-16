---
title: WASM data format
description: The byte-level contract a sandboxed guest uses to call a host capability — the (ptr,len) ABI and the JSON envelope on top of it.
---

Everything untrusted — the JavaScript interpreter, a compiled skill, a plugin — runs as a
**WebAssembly guest** on wazero. A guest cannot open a socket or a file itself; it reaches a
capability only by calling a **host function** the host handed it. This page documents the
exact data that crosses that boundary.

## The one import a guest sees

The host mounts a single import module, `nocturn`. Its members are exactly the granted host
functions and nothing else — a capability the guest was not granted is **structurally absent**,
so it cannot even be named, let alone called ("unforgeable by absence").

The JavaScript interpreter, for example, declares exactly one import: `nocturn.call`.

## The `(ptr, len)` ABI

Data crosses as raw bytes in the guest's linear memory. The call convention is the standard
host↔wasm contract (the same one QuickJS, Extism and friends use):

```
guest calls   nocturn.<name>(reqPtr: u32, reqLen: u32) -> u64
```

1. The guest writes its **request bytes** into its own memory and passes `(reqPtr, reqLen)`.
2. The host reads `reqLen` bytes at `reqPtr` (wazero bounds-checks the read) and **copies them
   out immediately** — a memory view is only valid for the duration of the call.
3. The host runs the capability and produces **response bytes**.
4. The host allocates space for the response *inside the guest* via the guest's exported
   `malloc`, writes the bytes there, and returns a **packed pointer**:

   ```
   return value (u64) = (addr << 32) | size
   ```

5. The guest unpacks `addr` and `size`, reads `size` bytes at `addr`, then `free`s `addr`.

A return value of `0` means an empty response. The guest must therefore export `malloc` and
`free` for the host to place the response.

:::note[Why copy-out immediately]
wazero's `Memory.Read` returns a *view*, not a copy, and the guest's memory can be reallocated
by a later `malloc.grow`. The host copies the request bytes out the instant it receives them and
never holds a view across the call. Within a single host call the guest is suspended, so this is
race-free.
:::

## The JSON envelope (for `code.run`)

On top of the raw byte ABI, the script gate speaks JSON. When a script calls
`nocturn.call(tool, args)`, the request bytes are:

```json
{ "tool": "http.read", "args": { "url": "https://example.com" } }
```

The host dispatcher looks `tool` up in the **same tool registry the model uses**, runs its
`Invoke` (which is fully gated — broker + approval), and returns the tool's result string as the
response bytes. So a script reaches an effect through the identical authorization path as the
model; the interpreter is never rebuilt to add a capability.

**Errors** come back as a response prefixed with `error: `. The guest's binding turns that into
a JavaScript exception, so a denied or failed effect raises in the script instead of crashing the
host. Pure computation that never calls the gate performs no effect and needs no approval at all.

## What this buys

The byte boundary is deliberately tiny and uniform: one import module, one `(ptr,len)` calling
convention, one JSON envelope. Every capability is just a Go function on the host side of that
boundary — which is exactly why each one is auditable, wrappable, and revocable in one place.
