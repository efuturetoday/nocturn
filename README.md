<div align="center">

<img src="assets/mascot.png" alt="The Nocturn mascot — a sleeping gopher in a nightcap and sleep mask, drifting through a starfield." width="220">

# Nocturn

**An AI assistant that works autonomously — but stops to ask for your approval on your phone before
doing anything you can't take back.**

One single Go binary. No cloud, no database, no runtime required.

[![Documentation](https://img.shields.io/badge/docs-nocturn-6E56CF)](https://efuturetoday.github.io/nocturn)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**[📖 Read the documentation →](https://efuturetoday.github.io/nocturn)**

</div>

---

> [!WARNING]
> **This is an alpha.** Expect bugs, and expect change — everything here is still moving: the UI, the
> UX, the tooling, the wire protocol and the feature set alike. Some of that movement will break what
> you set up. The security boundaries are the part that gets the scrutiny; everything around them is
> going to get better fast, which is another way of saying it is not finished. Issues and pull
> requests are exactly what this phase is for.

## ⚡ Why is Nocturn different?

Most AI agents have full access to your system by default simply because they run on Node or Python.
Nocturn flips this principle: a capability is *absent* until you hand it over, never present until
something denies it. It provides a solid technical foundation that harmonizes the execution of
plugins and scripts while keeping boundaries strictly enforced.

* **Your smartphone is the gatekeeper:** Before the AI reaches the network or modifies a file —
  sending that email, writing that document — an approval request pops up in the companion app on
  your phone. You retain absolute control.
* **Zero-Trust Sandbox:** Foreign code and AI-generated scripts run inside an isolated WebAssembly
  (WASM) sandbox. No filesystem access, no network access, no environment variables — unless
  explicitly handed over.
* **Secrets stay secret:** The language model never sees your API keys or passwords, yet it can still
  drive an API that needs one. It just asks for a URL; the host injects the token in the background
  as the request crosses the boundary.
* **Plug & Play:** No heavy dependencies, no containers. Just download the binary and run it.

## 🛠 What can Nocturn do?

Nocturn isn't just a chatbot; it's a capable assistant that handles tasks in the background (or on a
schedule) while keeping your machine safe:

* **Process files & code:** Let the AI write and execute scripts (`code_run`) to analyze data or
  automate workflows — safely locked inside the WASM sandbox.
* **Connect your infrastructure:** Integrate your email, calendar, local files, or external APIs via
  MCP servers and plugins. Everything is modular and extensible.
* **Autonomous routines:** Schedule agents via cron jobs (e.g., "Summarize my unread emails every
  morning at 7 AM") while you sleep.
* **Local memory:** Nocturn remembers important details about your preferences in a durable, local
  state.
* **Voice control:** ![experimental](https://img.shields.io/badge/experimental-F5A623) Talk directly
  to Nocturn via your machine or connected satellite speakers.

---

## 🚀 Quickstart

```bash
# Just the binary — no Node, Python, or containers to install
go build ./cmd/nocturn

# Configure an OpenAI-compatible endpoint
cp .env.example .env

# Start the local terminal chat
./nocturn

# Or start the server your phone and satellites connect to
./nocturn serve
```

*(Nocturn does not ship with a model — point it at any API you control. Everything it knows stays
strictly local in a `nocturn-data/` folder.)*

## 📱 The companion app

The app is what answers an approval when you are nowhere near the machine: it finds nocturn by name
on your own network — no account, no relay — and shows the ask as the gate recorded it, never as the
conversation phrased it. It is also a full client: the same chats the terminal is talking to,
and every tool call the model made, timed and attributable.

Screenshots and the walkthrough are in the
**[app guide](https://efuturetoday.github.io/nocturn/guides/the-app/)**.

<table>
<tr>
<td align="center"><img src="docs/public/qr/testflight.png" width="200" alt="TestFlight"></td>
<td align="center"><img src="docs/public/qr/android.png" width="200" alt="Android APK"></td>
</tr>
<tr>
<td align="center"><a href="https://testflight.apple.com/join/TdMWnxYF">Public beta</a></td>
<td align="center"><a href="https://github.com/efuturetoday/nocturn/releases/latest">Latest release</a></td>
</tr>
</table>

No nocturn of your own yet? On the first screen tap **Enter server manually** and enter `demo` as the
host — sample data, held entirely on the device.

## 🧱 Architecture

Nocturn is built on two strictly separated core components:

* **`agentkit/` (the engine):** a completely independent, policy-blind turn loop with zero external
  dependencies.
* **`internal/` (the boundary):** the security system that decides what the engine is allowed to do —
  WASM sandbox, encrypted vault, out-of-band approvals.

* **[Full documentation](https://efuturetoday.github.io/nocturn)** — architecture, threat model,
  plugin development
* **[examples/](examples/)** — a workspace with one of everything: an agent, a skill, a plugin, an
  MCP server, memory, documents to search
* **[ADRS.md](ADRS.md)** · **[CONTRIBUTING.md](CONTRIBUTING.md)** · **[SECURITY.md](SECURITY.md)**

## 🤖 How this was built

The idea, the architecture, the security boundaries and the threat model are human. So are the
constraints the code is held to: the tech stack, the dependency budget, the code style, and every
decision recorded in **[ADRS.md](ADRS.md)**. And the mantra:

> One aspect at a time — clarify, build, prove stable. Explicit over implicit. No sprawl, no cruft,
> no backward-compat ballast in greenfield.

Inside those constraints, Nocturn was built with frontier models as a pair programmer — and almost
every line here was typed by one. Not a handoff: each piece was talked through before it was written,
the reasoning went both ways, and no diff landed unread. That is what made this pace possible. A
system this size stays coherent because someone holds its shape while the implementation moves fast.

Agentic contributions are welcome on exactly those terms — bring what your agent wrote, once you have
read the diff yourself.

## License

[Apache-2.0](LICENSE).
