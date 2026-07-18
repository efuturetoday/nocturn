# Nocturn — Projektwissen (CLAUDE.md)

> Destilliertes, **nicht-ableitbares** Wissen: Vision, Threat-Model, Muster, Pitfalls.
> Lies das zuerst, bevor du weiterbaust.
>
> **Single sources of truth — hier NICHT duplizieren:**
> - Was ein Paket tut → **`go doc ./internal/<pkg>`** (die Paket-Doc-Kommentare sind die
>   destillierte Per-Paket-Wahrheit; §4 ist nur ein Index, keine Nach-Transkription).
> - Warum wir X statt Y gewählt haben → **`ADRS.md`** (die Entscheidungs-Records).
> - Was schon passiert ist → **git log** (keine „Erledigt"-Historie hier).
>
> Vollständiger Ur-Plan: `~/.claude/plans/ich-m-chte-eine-openclaw-foamy-tulip.md`.
> Offene Fragen/Entscheidungen: **`FRAGEN.md`** (pflegen, wenn was aufkommt).

---

## 1. Was Nocturn ist

Ein **sicherer persönlicher AI-Assistent** in **Go** — Gegenentwurf zu OpenClaw
(„sloppified AI crap": unsicher, komplex, schwere UI). Ein einzelnes Binary,
kein Fremd-Runtime, das ein LLM mit **capability-gebrokerten, human-in-the-loop-
gegateten** Werkzeugen orchestriert.

### Vision — 4 Säulen (Nordstern)
1. **Simplicity / easy deployment, ohne Runtime** — eine Go-Binary, kein
   Node/Postgres/Docker/CGo/Fremd-Runtime.
2. **Nachhaltiges Ökosystem, DevEx super, polyglot** (JS/TS/Rust/Go).
3. **Kleine begleitende App** (später Tauri).
4. **Klein, fokussiert, sicher, überschaubar** — minimale TCB, nicht alles voll
   ausgebaut, aber beherrschbar.

---

## 2. Abgrenzung — was wir anders machen

| | OpenClaw | IronClaw (nearai, Rust) | **Nocturn** |
|---|---|---|---|
| Stack | TS/Node | Rust + wasmtime + **Postgres** + Node-WebUI | **Go, single binary, wazero** |
| Isolation | keine (exec/bash) | WASM-Component + Vault + TEE | WASM (wazero) Zero-Authority |
| Effekt-Gatung | schwache Allowlists | statische Per-Tool-Allowlist | **dynamisches Ziel-Gating + HITL** |
| Human-in-the-Loop | iOS/Watch, **exec-only, abschaltbar**, in-band | in-band `ask`, **Auto-Approve default AN** | **verpflichtend, out-of-band, nicht abschaltbar** |
| Polyglot-Skills | Markdown (kein Code) | **Rust-only, kein PDK** | polyglot geplant (Skill-Schicht offen) |
| TCB | groß | 54 Crates | **minimal** (wazero + go-openai(0 transitiv) + godotenv) |

**Der verteidigbare Winkel:** *verpflichtende, nicht abschaltbare Out-of-band-
Freigabe auf einem zweiten Gerät, an JEDER Trust-Boundary, WASM-isoliert, als
einzelne Go-Binary ohne DB/Cloud.* Weder OpenClaw noch IronClaw haben das
(beide: „Mensch raus / optional / in-band"). **Live gegen ntfy.sh + echtes iPhone
verifiziert.**

**Zwei Bedrohungsklassen → zwei Verteidigungen (Kern-Erkenntnis):**
- Bösartiger Skill-*Code* / Supply-Chain → **WASM-Sandbox** (isoliert den Code).
- Prompt-Injection missbraucht *legitime* Tools → **Broker + Out-of-band-HITL**
  (isoliert die *Wirkung*). In-band-Freigabe liegt im selben Trust-Domain wie die
  Injection → deshalb **separates Gerät**.

---

## 3. Architektur — die Zwiebel (Schicht für Schicht gebaut)

Von innen (0) nach außen — jede Schale ruht auf der darunter, jede war stabil +
getestet, bevor die nächste kam:

**Die Schalen — von innen (0) nach außen (7), jede ruht auf der darunter:**

```
7  cmd/nocturn      TUI (parameterlos) = das ganze Interface; app.go = Prozess-Spine,
                    stack.go = ein isolierter Stack pro Workspace
   workspace        Composition-Aggregat: OWNS guard/tools/skills/agents/grants pro ws;
                    mintet die Charters (RootCharter/AgentCharter)
   chat · agent     Chat = DIE Konversations-Einheit (Charter-konstruiert, ein Turn-Loop,
                    Once/Store/Manager); agent = nur Deklaration + Cron-Scheduler
   plugin · mcp     sandboxed Plugins · remote-MCP-Client — beide über die geteilte Registry
6  llm              go-openai-Adapter, native tool_calls, SSE  (erfüllt brain.Model)
5  brain            agentischer Loop, Model-Port; Tokens/Tools/Reasoning → internal/activity
4  gateway          Guard.Authorize = die eine Pipeline (Cage ∩ Grants ∩ Policy → HITL)
                    Effekt-Adapter LEBEN AUSSERHALB: netcap/filecap/notifycap/… (je *Guard)
3  hitl             Out-of-band-Freigabe, HMAC-Single-Use-Token; ntfy-Transport
2  secret           Store (nur Präsenz) + Vault (verschlüsselt) + Injector + Leak-Scanner
1  capability       Broker deny>ask>allow (fail-closed) + Epoch/Rate/Window/Cage/Grants
0  sandbox          Zero Authority + WASI + Standard-ABI + Härtung (wazero)
```

### Request-Fluss

```
   User "hol mir X"  (TUI, parameterlos)
        │
        ▼
   cmd/nocturn ── lädt .env; baut den Prozess-Spine (master key, HITL-Engine, LLM-Client);
        │          workspace.Open setzt PRO Workspace den isolierten Stack zusammen
        ▼
   chat.Chat ── serialisierter Turn-Loop (Submit rein / Subscribe raus);
        │        turn = Scope.Bind(ganze Authority) + Skills + Budget — DIE eine Zeremonie
        │
        ▼
   brain  ── agentischer Loop: Model fragen → Tool-Call | Antwort → Tool → zurück
        │        Model = Port; Tokens/Tool-Events/Reasoning streamen via internal/activity (ctx-Sink)
        ├──▶ llm  ── Adapter über go-openai, NATIVE tool_calls, SSE. Naht: brain.Model ⟵ llm.Client
        ▼
   tool.Registry ── ein Effekt-Tool (netcap/filecap/…) baut capability.Call{Family, Write, Target}
        │
        ▼
   gateway.Guard.Authorize ── DIE eine Pipeline (reiner Komponierer, kein per-session-State):
        │   Cage-Kette ∩ stehende capability.Grants ∩ Policy.Evaluate(call, Env{Now,Epochs,Rate})
        │      → Allow: weiter · Deny: ErrDenied · Ask: HITL
        ├──▶ hitl.Engine ──[attended Stream ODER ntfy → 📱]──▶ (Approve/Deny, signiertes Token)
        ▼
   secret.Injector ── Bearer host-seitig an der Grenze injiziert (Gast sieht ihn nie)
        ▼
   echter Effekt (HTTP/DNS/File/…) → Egress/Ingress-Leak-Scan → Ergebnis zurück ins Brain
```

> Detail pro Schale: `go doc ./internal/<sandbox|capability|secret|hitl|gateway|brain|llm>`.

---

## 4. Paket-Index (`internal/`)

> **Nur ein Index — was ein Paket wirklich TUT steht im Paket-Doc-Kommentar: `go doc
> ./internal/<pkg>`.** Hier nichts duplizieren (sonst driftet es). Ein Einzeiler pro Paket,
> nach Rolle gruppiert.

**Sicherheitskern (reine Entscheidung + Grenze):**
- `capability` — der Broker: `Call{Family,Write,Target}` gegen `Policy` → allow/ask/deny; `Cage`, `Grants`, `EpochRegistry`, `RateLimiter`, `Window`. Kein I/O.
- `gateway` — `Guard.Authorize` = die eine Pipeline (Broker + HITL + Grants); `Do` = authorize-then-execute; `Scope` = widerrufbare Epoche. Effekt-Adapter leben außerhalb.
- `secret` — Store (nur Präsenz) + verschlüsselter `Vault` + `Injector` (host-owned Cred-Injektion) + `Scanner` (bidirektionaler Leak-Scan).
- `hitl` — Out-of-band-Freigabe-Engine, HMAC-Single-Use-Token; Sub-Paket `hitl/ntfy`.
- `sandbox` — wazero-Gast unter Zero Authority + WASI + Standard-ABI + Härtung (Memory-Cap, Wall-Clock-Deadline); gated selbst nichts.
- `deadline` — pausierbares Execution-Budget im ctx (HITL-Wait zehrt nicht das Timeout).

**Tool-Bus + Loop:**
- `tool` — der neutrale Tool-Bus (`Tool`/`Spec`/`Registry`), stdlib-only Leaf; trennt Effekt-Provider von Consumern (kein Import-Zyklus).
- `brain` — der agentische Loop; `Model`-Port, `Conversation`, parallele Tool-Calls.
- `llm` — OpenAI-kompatibler Adapter (go-openai, native tool_calls, SSE) → erfüllt `brain.Model`.
- `activity` — ein ctx-getragener Sink für Live-Aktivität (Antwort-Tokens, Reasoning, Tool-Events); one-way Observability, keine Domänendaten.

**Effekt-Capabilities (jede = kleiner Typ mit `*Guard`):**
- `netcap` — `http.read`/`http.write`, `dns.resolve`, `ping` (icmp).
- `filecap` — `file.read/list/stat/search` + `file.write/remove/move`, confined auf einen Workspace-Root (Target = Pfad).
- `notifycap` — `notify` (proaktiv den User erreichen; still, host-owned Kanal, Leak-Scan + Rate).
- `remindcap` — `remind`/`remind.list`/`remind.cancel` (persistent, feuert ein `notify`).
- `wakecap` — `wake` (Agent plant seine eigene Wieder-Aufwachung; ungegated, geboundet).
- `timecap` — `time.now` (ungegated, Null-Autorität; der Gast hat keine Wall-Clock).

**Interpreter · Plugins · MCP:**
- `script` — echter JS-Interpreter (QuickJS→wasm) auf der Sandbox; Tool `code.run`; ein Host-Gate `nocturn.call` auf die geteilte Registry.
- `plugin` — installierte, sandboxed Plugins (`plugin.js`/`.wasm` + `plugin.json`-Manifest mit Cage); Ersatz für lokale MCP-Server.
- `mcp` — protokoll-reiner Client für remote-MCP (JSON-RPC/HTTP, stdlib-only, injizierter Transport).
- `mcpcap` — die gegatete Verbindungsschicht darunter (jeder JSON-RPC-POST = ein `http.write` durch den Guard).

**Kontext · Identität · Credentials:**
- `skill` — die Skills-Schicht (agentskills.io): Kontext, KEINE Tools, null Autorität; progressive disclosure via `skill.load`.
- `oauth` — host-managed OAuth2 (Loopback-PKCE + Refresh); Token nur host-seitig als Bearer injiziert.
- `approval` — durabler „schon reviewed"-Record für Plugins/MCP (kein Re-Prompt bei unverändert); KEINE Autorität.
- `grantstore` — file-backed `capability.GrantStore` (`<ws>/grants.json`, 0600) — die stehenden Rechte.

**Composition + Lifecycle:**
- `workspace` — das Composition-Aggregat: OWNS pro Workspace guard/tools/skills/agents/grants; `Open` baut den Stack; die EINZIGE Charter-Fabrik (`RootCharter`, `AgentCharter(name, autonomy)`). Persona via `PERSONA.md` (layered).
- `chat` — DIE Konversations-Einheit: `Chat` (unter einem `Charter` konstruiert; serialisierter Turn-Loop, Submit/Subscribe — der headless-Kern, den TUI/REST/Mobile gleich treiben; `turn` = die eine Zeremonie), `Once` (Wegwerf-Chat = ein Agent-Run), `Store` (+`Meta.Agent`, `Prune`) + `Manager` (N live Chats, `FireAgent` für Cron-Firings mit persistiertem Transcript).
- `agent` — nur noch DEKLARATION + Zeitplan: `Agent` (`<ws>/agents/<name>/agent.md`, `Matches`, `Discover`) und der Cron-`Scheduler`; Ausführung lebt in `chat` (Once/FireAgent), die Übersetzung in Autorität im `workspace` (AgentCharter).

`cmd/nocturn` — **die TUI ist das ganze Interface** (parameterlos, `go run ./cmd/nocturn [ws]`):
`app.go` (Prozess-Spine + bubbletea-Programm), `stack.go` (`shared`/`bound`, ein isolierter Stack
pro Workspace), `tui.go` (View, event-getrieben über den offenen `chat.Chat`), `plugins.go`/`mcp.go`/
`auth.go` (interaktives Wiring: Plugins, remote-MCP, Plugin-OAuth), `vault.go` (Unlock), `agents.go`/
`skills.go` (Startup-Reports), `theme.go` (Styles). Detail: `go doc`. `spike/extism`, `spike/javy` =
Wegwerf-Spikes (eigene go.mod).

**Dependencies (bewusst minimal):** Kern (`internal/`): `wazero` (pure Go, kein
CGo), `go-openai` (**0 transitive Deps**), `golang.org/x/sys`, `golang.org/x/net`
(nur `icmp` für `ping` in `netcap`), `golang.org/x/oauth2` (nur im `oauth`-Paket),
`github.com/petar-dambovaliev/aho-corasick` (nur im `secret`-Leak-Scanner),
`gopkg.in/yaml.v3` (nur im `skill`-Paket, SKILL.md-Frontmatter;
in Go kein RCE-Deserialisierungs-Risiko, Input size-capped + typed decode; `check.v1`
= test-only). `cmd/`: `godotenv`, **charm** (bubbletea/lipgloss/bubbles) — nur
Präsentation, berührt die **Trusted-TCB nicht**. **Verworfen:** langchaingo (290 Deps,
bringt eigenen Agent-Loop der unsere Sicherheit umgeht).
**Dev-Tools:** `wat2wasm` (brew wabt) für den WAT-Gast; **`wasi-sdk` + quickjs-ng-Checkout** zum Neubauen des Interpreter-`.wasm` (`internal/script/qjs/build.sh`, nur bei Shim-Änderung — das gebaute `.wasm` ist committet); `javy` (Spike). Kein Runtime-Dep: das Binary bleibt pure Go/wazero, kein CGo.

---

## 5. Zentrale Design-Entscheidungen (ADRs)

**Ausgelagert → `ADRS.md`.** Dort liegen die Entscheidungs-*Records* (das *Warum*: X
statt Y) — ADR-1…10 plus LLM-Provider, Trust-Grenze (Variante A), wazero-vs-Wasmtime
und PORTICO. Ergänze einen ADR dort, wenn du eine tragende Entscheidung triffst oder
umkehrst. Kurzform des resultierenden Sicherheitsmodells: siehe §8.

---

## 6. Muster & Code-Style (durchgängig eingehalten)

- **Explizite Konstanten statt überladener Nullwerte, fail-closed.** Vergessenes
  Feld darf nie still „alles erlauben / permanent / wildcard" heißen.
  - `capability.Wildcard` (`"*"`) — leer matcht *nichts*; leeres `HostGlob` matcht
    keinen Host-tragenden Call → man kann `net.fetch` nicht aus Versehen für alle
    Hosts freigeben.
  - `capability.Permanent` — `Epoch:0` = unset = fail-closed; Permanenz explizit.
  - `hitl` Outcome-Nullwert = `Denied`.
- **Kein Backward-Compat-Cruft in Greenfield.** Alte API ersetzen + alle (eigenen)
  Call-Sites migrieren; keine redundanten Wrapper stehen lassen (das ist Tech-Debt).
  Bsp: `Evaluate`/`EvaluateInEpoch`/`EvaluateWith` → *eine* `Evaluate(call, Env)`.
- **Ports & Adapters.** `brain.Model` = Port; `llm.Client` = Adapter über go-openai
  → Brain provider-agnostisch + mock-testbar. `hitl.Notifier` = Port; `ntfy` = Adapter.
- **Kein God-Object.** `Guard` = geteilte Autorisierungs-Pipeline; **Capability-Gruppen**
  (`Net`) sind kleine Typen mit `*Guard` + eigenen Deps. Neue Capability = kleine
  Methode / kleiner Typ, **kein** wachsendes Feld-Monster.
- **Rollen-Namen für Felder** (`Secrets`, `Approvals`, `Policy`, `Rate`) statt
  generisch/typ-benannt (`Vault`, `Engine`). Paketnamen unzweideutig (`llm`, nicht `model`).
- **Funktionale Options** für Konfiguration (`ntfy.WithAuth`, `RateLimiter.WithLimit`).
- **Zwiebel-/Mauer-Bau:** einen Aspekt klären → in Code gießen → als stabil beweisen
  → erst dann die nächste Schicht. „Untere Schale nicht anfassen" = *stabil halten*,
  nicht *toten Code behalten*. Kein quer-schneidender Wildwuchs.

---

## 7. Pitfalls & Tweaks (real getroffen)

- **wazero `Memory.Read` gibt einen View zurück, keine Kopie.** Bytes **sofort
  rauskopieren** (`string(buf)`) bevor die Host-Function zurückkehrt; nie über den
  Aufruf hinaus behalten (`memory.grow` realloziert). Innerhalb eines Host-Aufrufs
  ist der Gast suspendiert → race-frei. Eine Instanz = eine Goroutine (Instanzen
  sind nicht nebenläufig-sicher).
- **`go mod tidy` entfernt Deps, die (noch) niemand importiert.** godotenv wurde
  geprunt bis `main.go` es nutzte → dann `go get` + `tidy`.
- **`.env` ist gitignored** (Key nie im Code); `godotenv.Load()` lädt aus CWD, echte
  Env-Vars gewinnen. `.env.example` committen.
- **ntfy-Kanal-Auth ist schwach** (Topic-Name ist öffentlich) → Sicherheit kommt aus
  dem **HMAC-signierten, single-use, expiry-gebundenen Token** (Outcome *im* signierten
  Payload → Deny kann nicht zu Approve gefälscht werden). Response geht an ein
  *anderes* Topic (Daemon hört nur ab, kein eingehender Port).
- **LLMs sind nie 100% feuerfest.** Robustheit = strukturierte `tool_calls` (kein
  Text-Raten) + Schema als Leitplanke + **eigene Arg-Validierung + Retry** (Fehler
  wird ans Modell zurückgespeist).
- **freellm-Endpoint:** OpenAI-kompatibel, Model `auto` (umgeht Cooldowns einzelner
  Modelle); `FREELLM_BASE_URL`/`_API_KEY`/`_MODEL` in `.env`. War hinter Authelia
  (303→SSO), jetzt offen.
- **Tool-Call-History nativ (`tool_call_id`-Plumbing):** ein Assistant-Turn trägt seine
  `tool_calls` (mit id) nativ, ein Ergebnis ist eine `role=tool`-Message mit `tool_call_id` —
  Ergebnisse sind ihren Calls **per id** zugeordnet, nicht positionell/textuell. `brain.ToolCall.ID`
  + `brain.Message.ToolCalls`/`.ToolCallID`; der llm-Adapter fängt die id (Fallback `call_<idx>`,
  falls der Endpoint keine liefert) und baut natives `tool_calls`/`role=tool` in `buildMessages`.
  (Früher: `[tool result] …`-Text; abgelöst.) Voraussetzung für parallele Tool-Ausführung.
- **`.gitignore`-Regeln an den Root ankern** (`/plugins/`, `/workspaces/`), sonst matcht
  `plugins/` **jedes** gleichnamige Verzeichnis in jeder Tiefe — `internal/workspace/` war so
  aus Versehen ge-ignored und **nie committet** (HEAD baute aus frischem Clone nicht).
- **gopls-Diagnostics können stale sein — der Compiler ist die Wahrheit.** „undefined: X" bei
  grünem `go build ./...` = stale Index (`go clean -cache` hilft beim nächsten Reload). Nie
  auf Verdacht Code umbauen; erst `go build`.

---

## 8. Sicherheitsmodell (Kurzform)

- **Zero Ambient Authority:** wazero-Gast bekommt nichts; jede Fähigkeit ist ein
  explizit gereichtes Host-Fenster → „unforgeable by absence".
- **Broker `deny > ask > allow`, deny-by-default, deny-wins.** Match-Bedingungen:
  Capability, Host(-Glob), lebende Epoche, Zeitfenster; danach Rate-Post-Check.
- **HITL:** `Ask` → out-of-band-Freigabe (Terminal *oder* Handy); Timeout/Deny =
  fail-closed. Verpflichtend, nicht abschaltbar für irreversible/externe Aktionen.
- **Vault:** Secrets host-seitig; Gast sieht nur Präsenz; Bearer an der Grenze injiziert.
- **Epoch:** Grants an Task gebunden, `reg.Close()` widerruft alles auf einen Schlag;
  Stale-Replay wird vor dem Effekt abgewiesen.

---

## 9. Testing-Konventionen

- **Externes Test-Paket** (`_test`) für die öffentliche API; **internes** (gleicher
  Paketname) nur für Unexportiertes.
- **Table-driven** wo viele Fälle (`TestPolicy_Evaluate`).
- **Fakes über Interfaces**: `Notifier`/`Model` mocken; ntfy/HTTP via `httptest`.
- **Blockierende Funktionen** (HITL, Streaming) mit Goroutine + Kanälen koordinieren
  (kein `sleep`); `-race` läuft grün.
- **Zeit-abhängige Tests via `testing/synctest`** (Go 1.25+, `synctest.Test` + echtes
  `time`, Fake-Clock im Bubble) — **keine** Clock-Injektion (`go.dev/blog/testing-time`);
  so testen `wakecap` + `capability` (RateLimiter). Produktions-Uhr-Injektion (`timecap.Clock`,
  `agent.Scheduler`) ist davon getrennt und legitim.
- Compile-Zeit-Asserts: `var _ brain.Model = (*llm.Client)(nil)`.

---

## 10. Dev-Workflow

> **Go-Skills nutzen, wann immer möglich.** Bei Go-Arbeit die installierten
> `cc-skills-golang`-Skills einsetzen — allen voran **`go-review`** vor jedem Commit von
> nicht-trivialem Go (prüft gegen Effective Go + Google Style Guide, zitiert die Regel),
> plus die themenspezifischen (`golang-concurrency`, `golang-error-handling`,
> `golang-testing`, `golang-naming`, …), wenn der Task sie berührt. `golang-how-to` ist der
> Orchestrator, der die passenden lädt. Nicht aus dem Gedächtnis stylen, wenn eine Skill die
> Regel kennt.

```bash
go build ./...            # alles bauen
go test ./internal/...    # alle Tests (race-clean)
go test -race ./internal/...
golangci-lint run         # (geplant)

# Sandbox-WAT-Test-Gäste neu bauen (nach Änderung):
wat2wasm internal/sandbox/testdata/echo.wat -o internal/sandbox/testdata/echo.wasm
wat2wasm internal/plugin/testdata/wasmprobe/plugin.wat -o internal/plugin/testdata/wasmprobe/plugin.wasm

# QuickJS-Interpreter-wasm neu bauen (nur bei Shim-Änderung; braucht wasi-sdk):
internal/script/qjs/build.sh

# Der Assistent (die TUI ist das ganze Interface, parameterlos):
cp .env.example .env   # FREELLM_API_KEY eintragen
go run ./cmd/nocturn
#   multi-turn Chat, Streaming, Markdown, Tool-Indicator; http.write = Ask → inline y/n;
#   Enter senden · Ctrl+J neue Zeile · Ctrl+N neue Session · ctrl+c quit
```

---

## 11. Status & nächste Schritte

> Was schon passiert ist steht in **git log** — hier nur der grobe Ist-Stand + was offen ist.

**Steht (race-clean getestet, live gg. iPhone verifiziert):** der ganze Sicherheitskern
(Schalen 0–5) · Effekt-Capabilities `netcap`/`filecap`/`notifycap`/`remindcap`/`wakecap`/
`timecap` · `code.run` (QuickJS auf der Sandbox) · `plugin`-System (JS/WASM, Cage-begrenzt) ·
remote-**MCP** (`mcp`/`mcpcap`, ADR-9) · **Skills**-Schicht · **OAuth** (nur aus Plugins) ·
**Workspace**-Aggregat (N isolierte Stacks/Prozess, `PERSONA.md` layered, mintet Charters) ·
**ein** `chat.Chat` als alleinige Konversations-Einheit (eine Turn-Zeremonie; Agent-Runs =
`chat.Once`/`FireAgent` mit persistiertem Transcript) · **TUI** (parameterlos, event-getrieben,
Streaming/Reasoning/Tool-Forest/`/ws`) · **Out-of-band-HITL** (attended Stream **oder** ntfy → Handy).

**Offen / als Nächstes:**
1. **Weitere Capabilities** (Mail, Kalender) — Muster: kleiner Typ + `*Guard`, kommt Modell/
   Skript/Plugin gleichzeitig zugute. (`exec` = **bewusst nie**, ADR-7 Bucket C bleibt leer.)
2. **Verteilung** (IronHub-Stil + Code-Signing) + **Skill/Plugin-Signing/Attenuation**
   (Ed25519) — der M2-Rest.
3. **Keychain-Backend** für `secret` (statt Prozess-Speicher).
4. **Härtung**: SECURITY.md, append-only Audit-Sink, Metriken; Task- statt Session-Epoche
   (PORTICO-Feinung).

**Arbeitsweise:** Zwiebelschalig — einen Aspekt klären → bauen → als stabil beweisen. Explizit
statt implizit. Kein Wildwuchs, kein Cruft, kein Backward-Compat-Ballast in Greenfield.

---

# Anhang — Recherche & Roadmap (dauerhaftes Wissen)

> Aus dem Ur-Plan hierher konsolidiert. Referenz, nicht täglich gebraucht —
> aber zu wertvoll, um in einer Plan-Datei zu verrotten. `★`-Zahlen/Timestamps
> sind grobe Richtwerte (Fetch-Summarizer konfabuliert Zahlen — vor externer
> Nutzung via GitHub-API prüfen). Alles Übrige ist repo-/primärquellen-verifiziert.
>
> **Hinweis:** Die **Wettbewerbs-Recherche (A/B/C, Positionierung)** ist der bleibende Teil.
> Die **Roadmap/Milestones (E) und die Beweis-Checkliste (F)** sind ein historischer
> Schnappschuss — für den echten Ist-Stand siehe **§11 + git log**, nicht die Häkchen hier.

## A — Wettbewerb (Deep-Dive)

### IronClaw (`nearai/ironclaw`) — der direkte Rivale, Benchmark
Rust-Reimplementierung von OpenClaw, 54-Crate-Workspace, Apache-2.0/MIT, ~12,5k★,
v0.29.1 (Juni 2026, **pre-1.0**). Illia Polosukhin (NEAR) assoziiert. Hosted = NEAR
AI Cloud (TEE).
- **Isolation:** Wasmtime 46, Component-Model + WASI Preview 2. WASM-Tools sehen nur
  **4 Host-Funktionen** (`log`, `now_unix_secs`, `workspace_read/write`); Netz über
  host-seitigen HTTP-Proxy. Per-Tool `capabilities.json`. Memory-Limit via
  `ResourceLimiter`, **CPU via Fuel** (100 M Instr.). Pipeline:
  `WASM→Allowlist→Leak-Scan→Credential→Execute→Leak-Scan→WASM`.
- **Stärken (nicht frontal angreifen):** (a) **Credential-Vault** `ironclaw_secrets`:
  AES-256-GCM, per-Secret HKDF-SHA256, domain-separated AAD, Low-Entropy-Guard, Secret
  nie im WASM-Memory — **Table-Stakes-Vorbild für uns**. (b) **Bidirektionales
  Leak-Scanning** (15+ Patterns, ein-/ausgehend). (c) Rust-Memory-Safety + reife
  Component-Isolation + Fuel. (d) Feature-Breite (MCP-Client, viele Provider/Channels,
  NL→WASM-Tool-Autogen, Hybrid-Vektorsuche).
- **Schwächen (verifiziert, schlagbar):** (a) **Kein Out-of-band/Phone-HITL**
  (`phone|twilio|push|sms` = 0 Treffer). „Approval" = persistenter Grant-Store
  `ironclaw_approvals` (Allow/Ask/Deny), **prompted den User nicht**, **Auto-Approve
  default AN**, „Ask" ist *in-band* (angreifbar). Background-Trigger → kein Mensch,
  System *denied* nur High-Severity. (b) `PolicyAction::Review` = **Stub**. (c)
  **Skill-Attenuation nicht geportet** (#5581), Capability-Katalog leakt (#5712),
  **kein Code-Signing**. (d) **Kein SECURITY.md**, private Vuln-Reporting aus (#6000).
  (e) Multi-Tenant-Leck (#5460), Audit-Sink-Lücken (#5640/#5428). (f)
  **Schwergewichtig:** Postgres 15 + pgvector + 54 Crates + Node/pnpm-WebUI; „einfach" =
  NEAR-Cloud/TEE-Lock-in. „reborn"-Rewrite läuft → Design ungesetzt.

### OpenClaw selbst — der HITL-Incumbent (überraschend)
- iOS-App + **Apple-Watch** reviewen/genehmigen pending **`exec`-Requests** vom Handy
  (`operator.approvals`, „first committed answer wins"). Echte Out-of-band-Freigabe —
  **bereits ausgeliefert**.
- **Schwächen (unser Konter):** nur `tools.exec` (nicht alle Boundaries) · **abschaltbar**
  („never stop on exec approvals") · TS-Monolith, **kein WASM-Sandbox**. → Wir:
  *verpflichtend, nicht abschaltbar, an ALLEN Boundaries, WASM-isoliert*.

### WASM-Sandbox-Tech (Isolations-Konkurrenz)
- **MS Wassette** — MCP-Server, läuft WASI-Components als Tools (Wasmtime). WIT→JSON-
  Schema-Automapping, OCI-Distribution **signiert (Notation/Cosign)**, deny-by-default,
  YAML-Policy. Sauberste standardkonforme Capability-Injektion, aber **„not production
  ready"**, Rust+Wasmtime+CGo, **kein HITL**.
- **wasmCloud/Cosmonic** — Capability-Injektion als typisierte WIT-Contracts (Link-Zeit,
  kein Ambient-Effekt). Schwergewichtig (Lattice), aber Linking-Modell = Blaupause.
- **Extism** — Plugin-Framework, **Go-SDK nutzt wazero (CGo-frei)** — direkter
  Präzedenzfall. Capability = Manifest. **Footgun:** `allowed_hosts: null` = ALLE Hosts;
  kein Signing/Registry.

### OpenClaw-Forks (Isolation, aber kein Out-of-band-HITL)
| Fork | Stack | Isolation | Schwäche |
|---|---|---|---|
| **NanoClaw** (`nanocoai/nanoclaw`) | TS, Docker | Container-pro-Chat-Gruppe, Vault | „Tamper-evident Log" nur Blog-Claim; Name mehrdeutig |
| **NemoClaw** (`NVIDIA/NemoClaw`) | TS-CLI + Python | **Real:** OpenShell (Landlock+seccomp+netns) + YAML-Policy | Sandbox ≠ VM; Approval lokal/policy |
| **ZeroClaw** (`elev8tion/zeroclaw`) | Rust | Gateway-Pairing (OTP+Bearer), deny-by-default Channel-Allowlist | „<5 MB" = 1 macOS-Run, keine Methodik; fragmentiert |
| **PicoClaw** (`sipeed/picoclaw`) | **Go**, Single-Binary, MCP | Multi-Arch | **Kein** Capability-/Security-Modell; ~95% agent-generiert |
| **nanobot** (`HKUDS/nanobot`) | Python + MCP | „safer workspace access" | **Kein** Sandbox/Gate/Approval; Name kollidiert |

### HITL-Player (in-app/desktop, nicht out-of-band)
- **Cline** (~58k★) — per-Tool-Approval, Auto-Approve-Toggles, `requires_approval`,
  Shadow-Git-Checkpoints. VS-Code-in-Editor, kein Sandbox/Out-of-band.
- **QwenPaw** (AgentScope) — „Tool Guard" YAML + `ShellEvasionGuardian`, Level
  STRICT/SMART/AUTO/OFF, Kernel-Sandbox, Skill-Scanner. Lokal, **kein** Push.
- **Goose** (Block, Rust) — Modi Autonomous/Manual/Smart/Chat-Only, per-Tool
  Always/Ask/Never. In-app.
- **OpenHands** (Python) — Confirmation-Mode (`WAITING_FOR_CONFIRMATION`), sauber
  getrennt `SecurityAnalyzer` (LLM taggt Risk) vs. `ConfirmationPolicy`. Coding, in-app.
- **MS Agent Framework** — per-Function `approval_mode`; Run gibt `user_input_requests`
  zurück, **App liefert den Kanal** — exakt der Hook für eine Phone-Schicht. Kein Transport.
- **Shannot** (`corv89/shannot`) — HITL via Syscall-Interception + virtuelles FS, Review
  in **TUI**. Lokal, nicht mobil.

### Out-of-band-/Phone-Approval — die Nische ist in 3 Fragmente zersplittert
- **Bolt-on-MCP** (`telegram-assistant-mcp`) — winzig/generisch, kein Sandbox.
- **Claude-Code-Hooks** (`claude-push`, `claude-ntfy-hook`) — `PermissionRequest`-Hook +
  ntfy-SSE mit Allow/Deny. Beweisen die Nachfrage, aber Coding-scoped, **Topic-Name =
  einzige Auth (schwach)**, un-sandboxed.
- **Enterprise-OAuth CIBA** (Auth0/Okta) — rigoroseste Out-of-band-Freigabe,
  standardisiert, aber **transaktions-scoped**, kein genereller Trust-Boundary. *Option:*
  CIBA als Transport für Standards-Glaubwürdigkeit.
- **MCP Elicitation** — das *richtige* Primitive (Tool pausieren bis User-Input), aber
  transport-agnostisch → jemand muss es ans Handy verdrahten.

### Positionierungs-Matrix
| Projekt | Stack | Isolation | HITL | Out-of-band | Verbindlich | Ops | Reife |
|---|---|---|---|---|---|---|---|
| **Nocturn** | Go+wazero | Capability, Zero Ambient | Broker-Gate | **Ja, erzwungen** | **Ja, nicht abschaltbar** | Single-Binary | Neu |
| IronClaw | Rust | WASM-Component+Vault+Fuel | Grant-Store | Nein | Auto-Approve AN | Postgres+54 Crates/TEE | Reif (pre-1.0) |
| OpenClaw | TS | keiner | iOS/Watch (exec-only) | Ja, optional | **Abschaltbar** | Node-Gateway | Sehr reif |
| Wassette | Wasmtime-Comp. | Zero-Authority | — | Nein | — | MCP-Server | Früh |
| NemoClaw | TS+Rust | Landlock+seccomp+netns | Policy | Nein | Policy | Kernel-Sandbox | Neu |
| Cline | TS/VS-Code | keiner | Per-Action | Nein | Optional | Extension | ~58k★ |
| OpenHands | Python | Docker | Confirmation | Nein | Optional | Docker | Hoch |

### Wo Nocturn gewinnt (und wo nicht)
1. **Nicht neu:** WASM-Sandbox (IronClaw/Wassette) **und** Phone-Approval (OpenClaw)
   existieren einzeln — nicht als Neuheit verkaufen.
2. **Verteidigbar = Kombination + Verbindlichkeit:** *verpflichtende, nicht abschaltbare
   Out-of-band-Freigabe auf separatem Gerät, an JEDER Trust-Boundary, WASM-isoliert,
   Single-Binary ohne DB/Cloud*. Andere: *sandboxen-und-automatisieren* (IronClaw) **oder**
   *fragen-nur-in-app* (Cline/OpenHands) **oder** *out-of-band-aber-optional-exec-only*
   (OpenClaw). Niemand macht Out-of-band zum **erzwungenen Default**.
3. **Table-Stakes zum Mitziehen:** Credential-Vault + **bidirektionales Leak-Scanning**
   (sonst wirkt IronClaw stärker).
4. **Glaubwürdigkeits-Siege gg. IronClaws Lücken:** erzwungene Attenuation (#5581),
   Code-Signing, SECURITY.md + getesteter Audit-Sink (#6000/#5640), starke Krypto am
   Approval-Kanal (ntfy-Bolt-ons haben nur ratbaren Topic).
5. **Framing:** *„IronClaw-Grade Tool-Isolation, aber mit erzwungener menschlicher
   Zustimmung an Trust-Boundaries — auf einem zweiten Gerät, nicht abschaltbar, in einer
   einzigen Go-Binary ohne Cloud."*

## B — OpenClaw Gap-Analyse (Bedrohung → unsere Antwort)

OpenClaw-Arch: **channel** (Messaging-Adapter) → **brain** (Loop, Memory) → **body**
(Tools: Browser/Shell/Cron). Skills = Plain Files via „ClawHub", model-agnostisch.

| # | Schwäche (dokumentiert) | Ursache | Nocturns Antwort |
|---|---|---|---|
| 1 | Prompt-Injection (~57% Robustheit); Web-/Nachrichten-Inhalt kapert Agent | LLM-Output steuert privilegierte Tools direkt | Broker + **HITL für irreversibel/extern**; LLM-Output = *untrusted* |
| 2 | **Exfil über Link-Previews** (PromptArmor): Agent baut Angreifer-URL, Preview holt sie | Ungebremster Egress | Kein Ambient-Netz; Egress gebrokert + **Leak-Scanning** + HITL für neue Ziele |
| 3 | Bösartige Skills / ClawHub-Supply-Chain (Cisco) | Skills ungesandboxt mit Host-Rechten | **WASM Zero-Authority**; Skills signiert + **Attenuation erzwungen** |
| 4 | Schwache Default-Configs (CNCERT) → Takeover | Ambient-Rechte, Opt-out | **Deny-by-default**: keine Capability ohne expliziten epoch-Grant |
| 5 | Exponierte Control-UI/Dashboards | Netz-erreichbare Web-UI | **Lokal-only**, Unix-Socket, keine Netz-Bindung; Keychain |
| 6 | Irreversible Fehlaktionen (MoltMatch) | Kein Freigabe-Gate für destruktive Ops | HITL zwingend für send/delete/pay/commit; Zeitfenster + Rate |
| 7 | Governance/„vibe slop" | Usability über Security | Kleiner auditierter Kern; append-only Audit; SECURITY.md |

**Kern:** Zwei unabhängige Bedrohungsklassen → zwei Verteidigungen. Bösartiger
*Code* (#3,#4) → **WASM-Sandbox**. Injection missbraucht *legitime* Tools (#1,#2,#6)
→ **Broker + Out-of-band-HITL** (Sandbox allein stoppt Injection nicht; in-band-
Freigabe liegt im selben Trust-Domain → **separates Gerät**).

## C · D — Runtime-Einordnung & Trust-Grenze → `ADRS.md`

Ausgelagert zu den Entscheidungs-Records: **wazero vs. Wasmtime** (WASIp1-only, kein
Component-Model/Fuel — warum trotzdem richtig), **PORTICO** (epoch-gebundene Capabilities,
Revoke = Epoche invalidieren — erster Schritt: `capability.EpochRegistry` + `gateway.Scope`),
und **Trust-Grenze Variante A** (Brain im Host, Skills/Tools im WASM). Siehe `ADRS.md`.

## E — Roadmap M0–M7 + Ziel-Layout

**Status:** M0–M5 stehen (Kern 0–5, Gateway, Brain, LLM, Binary, TUI, Out-of-band-HITL
live gg. iPhone verifiziert). M4-Rest (Leak-Scan) ist die **aktive** Aufgabe.

- **M0 Scaffold** ✅ — wazero führt Zero-Authority-Guest.
- **M1 Broker + erste Host-Fn** ✅ — deny>ask>allow, epoch-check. (`http_get` → real:
  `net.fetch`/`dns.resolve`.)
- **M2 Signierte + attenuierte Skills** ⬜ — Ed25519, erzwungene Attenuation, Beispiel-
  Skill (TinyGo→wasm). *Skill-Schicht geparkt (Extism vs. eigener Host+Javy).*
- **M3 Out-of-band-HITL** ✅ — Queue, signiertes Single-Use-Token, ntfy, nicht abschaltbar.
- **M4 Vault + Leak-Scan** ✅ — Credential-Injektion **host-owned + capability/host-scoped**
  (`secret.Injector`, Manual-Cred-Reject); **Leak-Scan** `secret.Scanner` (Tier1 exakt +
  Tier2 gitleaks/Aho-Corasick/Entropy, Egress-Block + Ingress-Redact). Offen: Keychain ⬜,
  Single-Use/Zeroize ⬜ (Go-Sprachgrenze, best-effort später), Leak-Scan-Verifikation bewusst ⛔.
- **M5 Brain** ✅ — Loop (Variante A), LLM-Adapter, Tool-Calls durch Broker.
- **M6 Tauri-Shell** ⬜ — Desktop-UI über Unix-Socket, Approval-Liste, kein Netz-Listener.
- **M7 Policy + Härtung** 🔷 — Zeitfenster/Rate ✅ (Primitive), Metriken/SECURITY.md/Audit-
  Sink ⬜, Security-Pass.

**Ziel-Repo-Layout (noch nicht erreichte Teile):** `cmd/nocturnd` (Daemon), `internal/
skill/` (loader + Ed25519 + Attenuation, registry), `internal/audit/log.go` (append-only,
getestet), `api/socket.go` (Unix-Socket-IPC), `skills/example-http/`, `desktop/` (Tauri),
`internal/secret/keychain.go`, `SECURITY.md`.

## F — Sicherheits-Beweis-Checkliste (was wir stets beweisen können müssen)

- **Zero-Authority:** Guest ohne `net`/`fs`-Grant kann keine Verbindung/kein FS öffnen
  (Link-/Trap-Fehler). ✅
- **Broker-Präzedenz + Epoch:** deny schlägt engere allow; first-match; unbekannte
  Capability → deny; abgelaufene Epoche → Stale-Replay abgewiesen. ✅
- **Attenuation (M2):** installierter Skill kann nachweislich nicht schreiben/HTTP/Shell
  (Konter zu IronClaw #5581). ⬜
- **Out-of-band-HITL-E2E:** Approve ⇒ Aktion+Audit; Deny/Timeout ⇒ getrappt;
  abgelaufenes/wiederverwendetes Token abgewiesen; Boundary-Policy nicht abschaltbar
  (Negativtest). ✅ (live gg. iPhone)
- **Vault/Leak-Scan (M4):** Secret nie im Guest-Memory; Egress mit Secret → geblockt;
  Ingress-Secret → geflaggt/redigiert. 🔷 (aktiv)
- **Exfil-Regression (OpenClaw #2):** Egress auf Nicht-Allowlist → geblockt; neue Domain →
  „ask" statt stiller Ausführung. ✅ (Gating) / 🔷 (Secret-in-URL: mit Leak-Scan)
- **Härtung (M7):** OOM-/Deadline-Guest sauber getrappt; Daemon bindet nur Unix-Socket,
  kein TCP (`lsof`). ⬜

## G — Referenzen & offene Punkte

**Referenzen auf der Platte:** `scrippy/internal/engine/extism/` (WASM-Host-Muster,
wazero), `my-tauri-app` / `f7svelte` (Tauri-Shells), `neura/aep-export-saas/.claude/
skills/golang-*` (Go-Style/Testing/Security).

**Offen:** Skill-Sprache Guests (TinyGo vs. Rust) · ntfy self-hosted vs. .sh + CIBA-
Adoption · Escape-Hatch wasmtime-go/Component-Backend · Task- vs. Session-Epoche
(PORTICO-Feinung, aktuell 1 Epoche/Session) · Keychain-Backend · Skill-PDK-Weiche
(Extism 34-Modul-TCB vs. eigener Host+Javy — Säule-2 DevEx vs. Säule-4 kleine TCB).
