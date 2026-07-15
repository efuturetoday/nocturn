# Nocturn — Projektwissen (CLAUDE.md)

> Destilliertes Wissen aus der gemeinsamen Entwicklung. Lies das zuerst, bevor du
> weiterbaust. Vollständiger Ur-Plan: `~/.claude/plans/ich-m-chte-eine-openclaw-foamy-tulip.md`.
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

```
┌──────────────────────────────────────────────────────────────────────────────────┐
│ 7  cmd/nocturn — TUI (parameterlos) · agent (Session) · plugin · oauth          │
│ ┌──────────────────────────────────────────────────────────────────────────────┐ │
│ │ 6  llm — go-openai-Adapter, native tool_calls                                │ │
│ │ ┌──────────────────────────────────────────────────────────────────────────┐ │ │
│ │ │ 5  brain — agentischer Loop, Model-Port, Streaming                       │ │ │
│ │ │ ┌──────────────────────────────────────────────────────────────────────┐ │ │ │
│ │ │ │ 4  gateway — Guard (die Pipeline) + Net (Fetch, Resolve)             │ │ │ │
│ │ │ │ ┌──────────────────────────────────────────────────────────────────┐ │ │ │ │
│ │ │ │ │ 3  hitl — Out-of-band-Freigabe, HMAC-Token (+ ntfy)              │ │ │ │ │
│ │ │ │ │ ┌──────────────────────────────────────────────────────────────┐ │ │ │ │ │
│ │ │ │ │ │ 2  secret — Store (nur Präsenz) + Credential-Injektion       │ │ │ │ │ │
│ │ │ │ │ │ ┌──────────────────────────────────────────────────────────┐ │ │ │ │ │ │
│ │ │ │ │ │ │ 1  capability — Broker deny>ask>allow, Epoch/Rate/Window │ │ │ │ │ │ │
│ │ │ │ │ │ │ ┌──────────────────────────────────────────────────────┐ │ │ │ │ │ │ │
│ │ │ │ │ │ │ │ 0  sandbox — Zero Authority + WASI + ABI + Härtung   │ │ │ │ │ │ │ │
│ │ │ │ │ │ │ └──────────────────────────────────────────────────────┘ │ │ │ │ │ │ │
│ │ │ │ │ │ └──────────────────────────────────────────────────────────┘ │ │ │ │ │ │
│ │ │ │ │ └──────────────────────────────────────────────────────────────┘ │ │ │ │ │
│ │ │ │ └──────────────────────────────────────────────────────────────────┘ │ │ │ │
│ │ │ └──────────────────────────────────────────────────────────────────────┘ │ │ │
│ │ └──────────────────────────────────────────────────────────────────────────┘ │ │
│ └──────────────────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────────────────┘
```

### Request-Fluss

```
   User "hol mir X"
        │
        ▼
   cmd/nocturn (CLI: run | chat) ── lädt .env (godotenv), baut alles zusammen
        │
        ▼
   brain  ── agentischer Loop: Model fragen → Tool-Call | Antwort → Tool → zurück
        │        Model = Interface (Port); Streaming via OnToken
        ├──▶ llm  ── Adapter über go-openai (OpenAI-kompatibel), NATIVE tool_calls,
        │            SSE-Streaming. Provider-Naht: brain.Model ⟵ llm.Client
        ▼
   gateway.Net ── Capability-Gruppe (Fetch, Resolve …), hält *Guard + eigene Deps
        │
        ▼
   gateway.Guard.Authorize ── DIE eine Pipeline:
        │   capability.Policy.Evaluate(call, Env{Now,Epochs,RateAllow})
        │      → Allow: weiter · Deny: ErrDenied · Ask: HITL
        ├──▶ hitl.Engine.Request ──[ntfy Publisher]──▶ 📱  (Approve/Deny)
        │         ◀── hitl.Engine.Resolve ◀──[ntfy Listener]◀── signiertes Token
        ▼
   secret.Store + Inject ── Bearer host-seitig an der Grenze injiziert (Gast nie)
        ▼
   echter Effekt (HTTP / DNS)  →  Ergebnis zurück ins Brain
```

### Schalen (jede stabil bevor die nächste kam)
0. **Zero Authority** (`sandbox`) — wazero-Gast ohne Freigabe kann *nicht mal starten*
   (ungranted import = strukturell abwesend).
1. **ABI-Fenster** (`sandbox`) — gebrokerte HostFunc-Imports; Daten via `(ptr,len)`
   über linearen Speicher (wazero bounds-checked); Standard-ABI (packed-ptr + Gast-`malloc`).
2. **Broker** (`capability`) — `deny > ask > allow`, deny-by-default, deny-wins.
3. **Epoch/Revocation** (`capability`) — task-gebundene, widerrufbare Grants (PORTICO).
4. **Secret-Store** (`secret`) — Gast sieht nur Präsenz, nie den Wert; Host injiziert.
5. **HITL** (`hitl`) — Out-of-band-Freigabe, HMAC-Single-Use-Token; `ntfy`-Transport.
   + Konsolidierung: `Broker.Evaluate` konsultiert Epoch + Zeitfenster + Rate.
   + Gateway (`Guard` + `Net`), Brain, LLM-Adapter, Binary (`run`/`chat`), Streaming.

---

## 4. Paket-Landkarte (`internal/`, minimal + überschaubar)

| Paket | Rolle |
|---|---|
| `sandbox` | **Die wazero-Schicht: Zero-Authority-Boden + generelle Guest-Engine** (Interpreter/Skills). `Run(ctx, guest, Config)`: gehärtet (Memory-Cap + Wall-Clock-Deadline trappt Runaways, #422), WASI-stdio, Workspace `WithDirMount(/work)` (Allowlist-by-construction), gebrokerte `HostFunc`-Imports über **Standard-ABI** (`nocturn.<name>(reqPtr,reqLen)→packed(addr<<32\|size)`, Host alloziert Antwort im Gast via dessen `malloc`). Der Sandbox **gated nichts** — Effekte sind caller-gelieferte HostFuncs, die ans Gateway delegieren. WAT-Test-Gäste (`echo`/`loop`/`logprobe`). |
| `capability` | Reine Entscheidung (kein I/O, stdlib-only). `Policy.Evaluate(Call, Env)` (deny>ask>allow, fail-closed). `EpochRegistry`, `RateLimiter`, `Window`. Konstante `Wildcard`, `Permanent`. **`Ceiling`** = komponierbare Obergrenze (Schnittmenge, ctx-**Kette** `WithCeiling`/`CeilingsFrom`/`WithinCeilings`; leere Kette = vacuously true = fail-**open**, bewusst — s.u.). **`Grants`** (ehem. `Context`) = Eigentümer des stehenden Permission-Sets einer Session/Workspace: session-scoped (epoch-gebunden) + `always` via `GrantStore`-Interface (I/O-frei gehalten); `Allows`/`Record(call, scope)`; Scope-Konstanten **`ScopeOnce`/`ScopeSession`/`ScopeAlways`**; ctx-Seam `WithGrants`/`GrantsFrom`. |
| `deadline` | **Pausierbares Execution-Budget im Context** (`WithBudget`/`PauserFrom`/`Pauser`), stdlib-only. Wie `context.WithTimeout`, aber die Deadline lässt sich **pausieren** und ist per ctx-Value auffindbar (Muster wie `WithEpoch`); verkettet sich mit einem Eltern-Budget (Pause/Resume propagiert hoch). `hitl` pausiert es während einer Out-of-band-Freigabe → der Menschen-Wait zehrt **nicht** das Sandbox-/Brain-Timeout auf (nur die HITL-TTL begrenzt ihn), danach läuft es mit Restbudget weiter. Cancel via `WithCancelCause` → Grund über `context.Cause` (DeadlineExceeded vs Canceled); Esc/Ctrl-C/TTL trappen weiterhin sofort. |
| `secret` | `Store` (Set/Exists, kind-agnostisch) + `GuestView` (nur Präsenz) + `Injector`/`Binding`/`Request` (host-owned Credential-Injektion, **capability + host scoped**; `capMatches`/`hostMatches`) + `Scanner` (bidirektionaler Leak-Scan: `ScanEgress`→`ErrLeaked`, `RedactIngress`→`[REDACTED]`; Tier1 exakter Vault-Wert encoding-robust + Tier2 gitleaks-Muster via Aho-Corasick + Entropy). |
| `hitl` | `Engine` (Request/Resolve, queue-then-execute), HMAC-`token`; Outcomes `Approved`/`ApprovedSession`/`ApprovedAlways`/`Denied`; Sub-Paket `hitl/ntfy` (`Publisher` push + `Listener` subscribe). |
| `tool` | **Der Tool-Bus (neutraler Vertrag, stdlib-only Leaf — importiert nichts Projekt-Internes).** `Tool`/`Spec` (Modell-sichtbare Aktion), die geteilte **`Registry`** (dispatch + `Add`/`Remove`/`Has`/`Specs`/`Invoke`, RWMutex, atomarer Call-id), und der Observer **`Event`** (`Start`/`End`-`Phase`, `ID`/`Parent` → Forest). Trennt Effekt-**Provider** (`netcap`/`script`/`plugin`) von **Consumern** (brain-Loop, TUI-Observer) → **kein Provider→Loop-Import mehr**. (Ehem. in `brain`; Typen entstuttert: `tool.Spec`, nicht `tool.ToolSpec`.) |
| `gateway` | **`Guard.Authorize` = reiner Komponierer** (hält keinen per-session-State): Ceiling-Kette (`WithinCeilings`, außerhalb = **hart deny, nie fragen**) → aktive `capability.Grants` (stehender Grant short-circuit) → `Policy.Evaluate` → HITL (`Once/Session/Always` → `Grants.Record`). `Do[T]` = authorize-then-execute. + `Net` (Capability-Gruppe): **`http.read`**/**`http.write`** (`capabilityForMethod`), `dns.resolve`; capability+host-scoped Credential-Injektion. `ErrDenied`, `ErrManualCredential`. (Depends **nicht** auf `brain`.) |
| `brain` | Agentischer Loop (schlank). `Model`-Interface (Port, `Next(...[]tool.Spec...)`), `Conversation`/`Message`/`Step`/`ToolCall`, `Run`, `OnToken` (Streaming). Tool-Calls einer Runde laufen **nebenläufig** (`sync.WaitGroup.Go`), Ergebnisse in Call-Reihenfolge. (Tool-Abstraktion + Registry → `internal/tool` ausgelagert.) |
| `script` | **Echter Interpreter auf der Sandbox: QuickJS (quickjs-ng) → wasm32-wasi**, embedded via `go:embed` (`qjs/nocturn-qjs.{c,wasm}`, Build `qjs/build.sh` mit wasi-sdk). `Runner.Run(ctx, source)` evaluiert JS-Source (stdin→eval→stdout); als Brain-Tool **`code.run`**. Der Gast deklariert **genau einen** Host-Import `nocturn.call(tool,args)` (+ `malloc`/`free`-Export für den packed-ptr-ABI); der Go-`dispatch` routet auf **dieselbe `tool.Registry` wie das Modell** (`Net.Tools()`) → jeder Effekt durch `Guard.Authorize` + HITL. Ein Gate = Reference-Monitor; neue Capability = Go-seitig, Interpreter unverändert. Reine Compute braucht null Caps; denied Effekt → JS-Exception (kein Host-Crash). `InterpreterGuest()` teilt das eingebettete QuickJS-`.wasm` an `plugin`. |
| `plugin` | **Sandboxed Ersatz für MCP-Server.** Artefakt (`plugin.js` auf dem geteilten QuickJS **oder** `plugin.wasm`) + Sidecar `plugin.json` (`Manifest`: `tools[]`, `requires[]`=Ceiling, `credentials[]`; `Validate()` fail-closed; `Load(dir)` **ohne** Ausführung). `Plugin.Tools()` → namespaced `<name>.<tool>` in die geteilte `tool.Registry`; **stateless** (frische Sandbox-Instanz pro Call, Cross-Call-State via `/work`). `runGuest` stempelt das **Plugin-Ceiling** in ctx (**einzige** Stelle, `SECURITY:`-Kommentar) → Effekte hart auf `requires` begrenzt; leeres Manifest = deny-all. `Host.Install`/`Uninstall` (eine HITL-Freigabe der Decke, **keine** Effekt-Grants; Uninstall lässt Context-Grants leben). |
| `llm` | OpenAI-kompatibler Adapter (go-openai), native `tool_calls`, SSE-Streaming. `Next(...[]tool.Spec...)` erfüllt `brain.Model`. |
| `oauth` | Host-managed OAuth (ADR-5). Google-Config + Loopback-PKCE-`Authorize`; refreshing `Source` (wrappt `golang.org/x/oauth2`-TokenSource) → an `secret.Injector` als Bearer-Quelle; Gast sieht das Token nie. |
| `agent` | **Session-Lifecycle-Owner.** `Session` bündelt `brain.Conversation` + geteilten `gateway.Guard` + geteilte `EpochRegistry` + die eigene **`capability.Grants`** + `GrantStore`. `Ask` stempelt die Grants via `capability.WithGrants` (+ optional Workspace-`Ceiling`) in ctx; `Reset` schließt die Epoche (widerruft Session-Grants), öffnet frische Grants+Conversation (`always`-Grants überleben); `Close` schließt die Epoche. Enthält **`GrantsStore`** (file-backed `capability.GrantStore`, `<config>/nocturn/grants.json`, 0600; ehem. in `gateway`). |

`cmd/nocturn` — **die TUI ist das ganze Interface** (parameterlos, `go run ./cmd/nocturn`);
`app.go` = Composition-Root (Stack zusammenbauen), `plugins.go` (walkt `./plugins/`, Install-
Review pre-TUI), `auth.go` (`wireGoogleCredential`), `tui.go` (View). Kein `run`/`chat`-Subcommand mehr.
`spike/extism`, `spike/javy` — **Wegwerf**-Spikes (Skill-Schicht-Entscheidung, geparkt), eigene go.mod.

**Dependencies (bewusst minimal):** Kern (`internal/`): `wazero` (pure Go, kein
CGo), `go-openai` (**0 transitive Deps**), `golang.org/x/sys`, `golang.org/x/oauth2`
(nur im `oauth`-Paket). `cmd/`: `godotenv`, **charm** (bubbletea/lipgloss/bubbles) — nur
Präsentation, berührt die **Trusted-TCB nicht**. **Verworfen:** langchaingo (290 Deps,
bringt eigenen Agent-Loop der unsere Sicherheit umgeht).
**Dev-Tools:** `wat2wasm` (brew wabt) für den WAT-Gast; **`wasi-sdk` + quickjs-ng-Checkout** zum Neubauen des Interpreter-`.wasm` (`internal/script/qjs/build.sh`, nur bei Shim-Änderung — das gebaute `.wasm` ist committet); `javy` (Spike). Kein Runtime-Dep: das Binary bleibt pure Go/wazero, kein CGo.

---

## 5. Zentrale Design-Entscheidungen (ADRs)

- **ADR-1: Ein Isolations-Tor = WASM/wazero.** Kein zweiter In-Process-Interpreter
  (goja) für Fremd-Skills (kein Speicher-Isolation, zweite Sicherheitstür = Wildwuchs).
  Polyglot via Kompilier-Stufen; JS/TS → QuickJS-in-WASM. **Code-Ausführung ist
  First-Class**; reine Compute-Transformation braucht **null Capabilities**.
- **ADR-2: Native Effekte = Host-Capabilities, nicht Gast-Code.** WASM kann keine
  Binaries exec'en. Gängiges (http, dns, ping) **nativ in Go** nachbauen; echtes
  `exec` nur als letzte Option (Allowlist + HITL + OS-Sandbox). Das Brain ruft
  Capabilities direkt durch den Broker.
- **ADR-3: Verteilung von IronHub geliehen, vereinfacht.** Git-Monorepo + `index.json`
  (url+sha256) + Release-Assets, *kein* OCI. Tool(wasm)/Skill(Markdown)-Split.
  **Nocturn-Plus: Code-Signing** (hat IronClaw nicht).
- **ADR-4: Dynamisches Ziel-Gating statt statischer Allowlist-Starrheit.** Bekannter
  Host → auto-allow; unbekannter → **verpflichtende Out-of-band-HITL**; + Zeitfenster/Rate.
- **ADR-5: Host-managed Credentials/OAuth; der Gast sieht nie das Token.** Host fährt
  OAuth-Flow + Refresh, injiziert Bearer erst an der Grenze; Gast hat nur `secret_exists`.
- **LLM-Provider:** **go-openai** (dep-frei) für den Chat-Call; **native tool_calls**
  (live bestätigt gegen freellm) statt geparstem Prompt-Protokoll; Args JSON-Schema-
  validiert (unmarshal + Retry-bei-Fehler). Extism/Javy-Skill-Schicht **geparkt**
  (beide gespiket) — die erste echte Capability (`net.fetch`) ist host-native und
  braucht sie nicht.

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
- **Funktionale Options** für Konfiguration (`ntfy.WithAuth`, `RateLimiter.WithClock`).
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
- **Injizierbare Uhr** (`WithClock`) für deterministische Zeit-Tests.
- Compile-Zeit-Asserts: `var _ brain.Model = (*llm.Client)(nil)`.

---

## 10. Dev-Workflow

```bash
go build ./...            # alles bauen
go test ./internal/...    # alle Tests (race-clean)
go test -race ./internal/...
golangci-lint run         # (geplant)

# Sandbox-WAT-Test-Gäste neu bauen (nach Änderung):
wat2wasm internal/sandbox/testdata/echo.wat -o internal/sandbox/testdata/echo.wasm
wat2wasm internal/script/testdata/gate.wat -o internal/script/testdata/gate.wasm

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

**Gebaut & getestet (race-clean), live verifiziert:** Sicherheitskern (Schalen
0–5) · Gateway (`http.read`/`http.write`, `dns.resolve`) · Brain (agentischer Loop, native
tool_calls, parallele Calls) · LLM-Adapter (freellm, Streaming) · **TUI** (parameterlos,
Streaming/Markdown/Tool-Indicator) · **Out-of-band-HITL gegen ntfy.sh + iPhone** · **End-to-End
mit echtem LLM, gestreamt** · **Plugin-System** (JS/WASM, Ceiling-begrenzt) · **OAuth** (Google).

**Offen / als Nächstes:**
1. **Weitere Capabilities** (`ping`, Mail, Kalender) — Muster: kleiner Typ + `*Guard`.
   Kommen Modell, Skripten **und** Plugins gleichzeitig zugute (ein Tool = alle Aufrufer).
2. **Workspace-Layer** — persistenter Workspace als `capability.Grants`-Eigentümer (eigene id +
   Persistenz + Workspace-`Ceiling`); die Grants/Ceiling-Mechanik ist schon Drop-in dafür gebaut.
3. **Verteilung** (IronHub-Stil + Code-Signing) + **Skill/Plugin-Signing/Attenuation** (M2-Rest,
   Ed25519). **Weg B (async Skript-Gate) — nur TODO**, s. FRAGEN.md.
4. **Keychain-Backend** für `secret` (statt Prozess-Speicher); Manifest-Hash-„approved"-Record
   (unverändertes Plugin = kein Boot-Prompt).

**Erledigt (Scripting-Frontier):** **Echter Interpreter auf der Sandbox** — `internal/script`:
QuickJS (quickjs-ng) → wasm32-wasi (wasi-sdk, embedded), Brain-Tool **`code.run`**. Effekte
über **ein generisches Host-Gate** `nocturn.call(tool,args)` → Dispatcher auf `Net.Tools()` →
`Guard.Authorize` + HITL. Damit ist die **M2-Weiche „Extism vs. eigener Host" zugunsten des
eigenen Hosts (QuickJS, ein Gate) entschieden** — minimale TCB (Säule 4), Erweitern nur
Go-seitig (Interpreter-`.wasm` unverändert). Race-clean getestet (Pure-Compute-Eval, Gate-
Dispatch durch echten Interpreter, denied-Effekt = fangbare JS-Exception, Runaway getrappt,
E2E gg. echtes `gateway.Net` + `httptest`).

**Erledigt (Plugin-System — sandboxed MCP-Ersatz):** `internal/plugin`. Artefakt (`plugin.js` auf
dem geteilten QuickJS **oder** `plugin.wasm`) + Sidecar `plugin.json`-Manifest; ein Runtime-Vertrag
(stdin `{tool,args}` → self-dispatch → Effekte via `nocturn.call` → stdout). Manifest `requires[]` =
**Ceiling** (Obergrenze, **kein** Auto-Grant): Install zeigt die Decke, **eine** HITL-Freigabe,
danach fragt der Agent **weiterhin pro Effekt** (du wählst einmal/Session/immer). Effekte hart auf
`requires` begrenzt (`runGuest` stempelt das Plugin-Ceiling — außerhalb = **deny ohne zu fragen**,
Anti-Injection). **Stateless** (frische Instanz pro Call, Cross-Call-State via `/work`). Tools
namespaced `<name>.<tool>` in die geteilte `tool.Registry` → identisch gegated/beobachtet wie
Modell-/Script-Calls. Race-clean + echt-QuickJS-E2E (in-Ceiling fragt→Session→still; out-of-Ceiling
hart-deny ohne HITL; Uninstall entfernt Tools, `always`-Grant überlebt).

**Erledigt (OAuth, ADR-5):** `internal/oauth` — Google Loopback-PKCE-`Authorize` (einmalige Consent-
Zeremonie, druckt URL) + refreshing `Source` über `golang.org/x/oauth2`; via `secret.Injector`
host-seitig als Bearer an der Grenze injiziert (Gast sieht das Token nie). `cmd` verdrahtet Gmail
vor dem TUI-Start.

**Erledigt (Autorisierung neu komponiert + `tool`-Extraktion):** (1) **`Guard` = reiner Komponierer**
— Ceiling-Kette (`capability.Ceiling`, Schnittmenge, ctx-getragen) ∩ stehende **`capability.Grants`**
(ehem. `Context`; session- + `always`-Tier über `GrantStore`) ∩ Base-Policy → HITL{once/session/always};
Guard hält keinen per-session-State mehr. `hitl.ApprovedAlways` + „Allow always"-Choice. (2) **Neutraler
Tool-Bus `internal/tool`** — `Tool`/`Spec`/`Registry`/`Event`/`Phase` aus `brain` ausgelagert (stdlib-only
Leaf); Provider (`netcap`/`script`/`plugin`) importieren **nicht mehr** `brain` (Inversion behoben). Typen
**entstuttert** (`tool.Spec`, `tool.Event`), Scope-Konstanten geprefixt (`ScopeOnce/Session/Always`),
`capability.Context`-vs-`context.Context`-Namensclash eliminiert. `GrantsStore` (file-backed) von `gateway`
→ `agent` verschoben (Lifecycle-Owner). `go build`/`vet`/`test -race ./...` grün; `gateway`/`capability`
haben 0 `brain`-Deps.

**Erledigt (Nebenläufigkeit):** (1) **native `tool_call_id`-History** (`brain.Message.ToolCalls`/
`.ToolCallID`, Adapter baut natives `tool_calls`/`role=tool`, Fallback-id `nocturn_call_<idx>`);
(2) **parallele Tool-Calls** — `brain.run` fächert die Calls einer Runde per `sync.WaitGroup.Go`
nebenläufig aus (kein Abbruch bei Fehler/Deny; Ergebnisse in Call-Reihenfolge → deterministische
History); (3) **serialisierte Freigaben** via `hitl.Serialize` (Mutex ums blockierende `Notify` —
auto-`Allow` läuft parallel, nur `Ask` serialisiert am Menschen, transport-agnostisch); (4)
**Observer mit id+parent** (`ToolEvent{ID,Parent}`, atomarer Registry-Zähler, ctx-getragene Call-id)
→ TUI-Observer von LIFO-`callStack` auf **Forest nach id** (nebenläufige Wurzeln + Verschachtelung).
Race-clean getestet (Barriere-Test beweist Parallelität; Forest-Bookkeeping headless). Der Mensch
bleibt bei gegateten Effekten der Flaschenhals — genau so gewollt.

**Erledigt (HITL-Wait pausiert Deadlines):** neues `internal/deadline` (pausierbares Budget im
ctx). `brain.ToolTimeout` und das Sandbox-Guest-Deadline nutzen jetzt `deadline.WithBudget`;
`hitl.Engine.Request` pausiert das Budget während der Freigabe (vor `Notify`, damit auch Notify-I/O
off-budget ist). Folge: eine langsame Out-of-band-Freigabe trappt den suspendierten Gast **nicht**
mehr (nur die HITL-TTL begrenzt den Menschen-Wait); danach läuft das Restbudget weiter. Gilt auch
für direkte Tools (`http.write`-Freigabe > 20 s). Esc/Ctrl-C/TTL trappen weiter sofort. Race-clean
getestet inkl. Money-Test: Freigabe (400 ms) überlebt ein 200-ms-Sandbox-Budget, Skript vollendet.
Fable-5-Review adressiert (brain `context.Cause`-Spiegel, `remaining=d`, untyped-nil-Pauser).

**Erledigt (Refactor):** **Epoch verdrahtet** — `agent.Session` als expliziter
Lifecycle-Owner; „Allow this session"-Grants sind epoch-gebunden und werden bei
`Reset`/`Close` widerrufen (Epoche schließen → `IsAlive`=false → Grant matcht nicht
mehr). Epoche fließt via `capability.WithEpoch(ctx)` durch `Ask → Conversation.Send
→ brain.run → tool.Invoke → Guard.Authorize`. TUI: **Ctrl+N = new session** (Grants
widerrufen + Chat leeren). Race-clean getestet (`internal/agent`, Gateway-Revocation).

**Arbeitsweise:** Zwiebelschalig, ein Aspekt klären → bauen → als stabil beweisen.
Explizit statt implizit. Kein Wildwuchs. Kein Cruft.

---

# Anhang — Recherche & Roadmap (dauerhaftes Wissen)

> Aus dem Ur-Plan hierher konsolidiert. Referenz, nicht täglich gebraucht —
> aber zu wertvoll, um in einer Plan-Datei zu verrotten. `★`-Zahlen/Timestamps
> sind grobe Richtwerte (Fetch-Summarizer konfabuliert Zahlen — vor externer
> Nutzung via GitHub-API prüfen). Alles Übrige ist repo-/primärquellen-verifiziert.

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

## C — wazero-Realität & PORTICO (ehrliche Runtime-Einordnung)

- **wazero ist WASIp1-only, kein Component-Model** (#2289 „not planned"), **kein Fuel**.
  Kosten ggü. Wasmtime: (a) keine typisierten WIT-Interfaces / kein WIT→Tool-Mapping →
  **eigene Manifest-/Schema-Schicht** (Extism-Stil) nötig; (b) WASIp1 gröber; (c)
  CPU-Grenze nur über **Context-Deadline + Memory-Page-Cap**; (d) Component-Tools von
  Wassette/wasmCloud laufen ohne Shim nicht.
- **Warum trotzdem richtig:** CGo-freie **Single-Binary** (Cross-Compile trivial), und
  **jede Capability = deine Go-Funktion** → Boundary maximal auditierbar, wrappbar,
  revozierbar.
- **PORTICO** (arXiv 2606.22504): Capabilities als **epoch-gebundene opake Handles**
  statt stehender Rechte — Grant an Task/Subgoal-Epoche binden, **Revoke = Epoche
  invalidieren**; jede Host-Function = Reference-Monitor (Stale-Replay vor dem Effekt
  abgewiesen). Attenuierbare **Biscuit/Macaroon**-Tokens (nur Verengung). → Die
  `EpochRegistry` + `agent.Session` sind der erste Schritt dieses Musters.
- **Escape-Hatch:** Capability-Interfaces abstrakt genug für ein späteres
  **wasmtime-go/Component-Backend**, falls Component-Portabilität hart wird.

## D — Trust-Grenze: Variante A (entschieden)

**„Skills/Tools im WASM, Brain im Host"** (aktuell umgesetzt). Alternative B (Brain +
Skills im WASM) offen gehalten.

| | A: Brain im Host (**gewählt**) | B: Brain + Skills im WASM |
|---|---|---|
| LLM-Keys | nie im Sandbox | müssen gebrokert werden |
| Isolation | Skill-Code isoliert; Broker+HITL gegen Injection-Wirkung | auch Loop isoliert |
| Komplexität | gering, idiomatisch Go | hoch (Loop+LLM-I/O über ABI) |
| Injection-Schutz | **identisch** (kommt aus Broker/HITL) | identisch |

**Begründung:** Injection-Abwehr kommt aus **Broker + Out-of-band-HITL**, nicht aus
der Loop-Position. A ist simpler, gleiche Netto-Sicherheit, Keys aus dem Sandbox.
Host-Function-Grenze so bauen, dass die Loop später bruchfrei nach B wandern kann.

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
