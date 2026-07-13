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
│ 7  cmd/nocturn — CLI: run | chat        (TUI folgt)                              │
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
│ │ │ │ │ │ │ │ 0  host — Zero Authority + ABI-Fenster               │ │ │ │ │ │ │ │
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
0. **Zero Authority** (`host`) — wazero-Gast ohne Freigabe kann *nicht mal starten*.
1. **ABI-Fenster** (`host`) — genau *ein* opt-in Host-Fenster; Daten via `(ptr,len)`
   über linearen Speicher; wazero bounds-checked.
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
| `host` | wazero-Host. `Run` (Zero-Authority), `RunWithLog` (ABI-Fenster), `RunWithBrokeredLog`, `RunWithHITLLog`. Test-Gäste in `testdata/` (`probe` Go-wasip1, `logprobe` handgeschriebenes WAT). |
| `capability` | Reine Entscheidung. `Policy.Evaluate(Call, Env)` (deny>ask>allow, fail-closed). `EpochRegistry`, `RateLimiter`, `Window`. Konstante `Wildcard`, `Permanent`. |
| `secret` | `Store` (Set/Exists, kind-agnostisch) + `GuestView` (nur Präsenz) + `Inject`/`Binding`/`Request` (Credential-Konzern). |
| `hitl` | `Engine` (Request/Resolve, queue-then-execute), HMAC-`token`; Sub-Paket `hitl/ntfy` (`Publisher` push + `Listener` subscribe). |
| `gateway` | `Guard.Authorize` (die eine Autorisierungs-Pipeline) + `Net` (Capability-Gruppe: `Fetch`, `Resolve`). `ErrDenied`. |
| `brain` | Agentischer Loop. `Model`-Interface (Port), `Tool`/`ToolSpec`, `Run`, `OnToken` (Streaming). |
| `llm` | OpenAI-kompatibler Adapter (go-openai), native `tool_calls`, SSE-Streaming. |

`cmd/nocturn/main.go` — CLI: `run <skill.wasm>` und `chat "<request>"`.
`spike/extism`, `spike/javy` — **Wegwerf**-Spikes (Skill-Schicht-Entscheidung, geparkt), eigene go.mod.

**Dependencies (bewusst minimal):** Kern (`internal/`): `wazero` (pure Go, kein
CGo), `go-openai` (**0 transitive Deps**), `golang.org/x/sys`. `cmd/`: `godotenv`,
**charm** (bubbletea/lipgloss/bubbles) — nur Präsentation, berührt die **Trusted-
TCB nicht**. **Verworfen:** langchaingo (290 Deps, bringt eigenen Agent-Loop der
unsere Sicherheit umgeht).
**Dev-Tools:** `wat2wasm` (brew wabt) für den WAT-Gast; `javy` (Spike).

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
- **Tool-Call-History vereinfacht:** wir mappen Tool-Ergebnisse als `[tool result] …`
  User-Beobachtungen (kein `tool_call_id`-Plumbing). Für strikte Multi-Tool-Convos
  später verfeinern.

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

# WAT-Gast neu bauen (nach Änderung an testdata/logprobe/log_probe.wat):
wat2wasm internal/host/testdata/logprobe/log_probe.wat -o internal/host/testdata/logprobe.wasm

# Assistent live (Streaming, gateway-bewacht):
cp .env.example .env   # FREELLM_API_KEY eintragen
go run ./cmd/nocturn chat --allow-fetch "Fetch https://example.com and summarize."
#   ohne --allow-fetch: net.fetch = Ask → Freigabe (Terminal), oder --ntfy fürs Handy

# Chat-Session-TUI (charm): multi-turn, Verlauf, thinking-Spinner, Tool-Indicator,
# Streaming, Markdown (Glamour), inline-Approval:
go run ./cmd/nocturn tui
#   Enter senden · Ctrl+J neue Zeile · net.fetch/dns = Ask → y/n inline · ctrl+c quit

# WASM-Skill durch den HITL-Log-Pfad:
go run ./cmd/nocturn run internal/host/testdata/logprobe.wasm
#   --ntfy --req-topic <a> --resp-topic <b>  → Freigabe aufs iPhone
```

---

## 11. Status & nächste Schritte

**Gebaut & getestet (race-clean), live verifiziert:** Sicherheitskern (Schalen
0–5) · Gateway (`net.fetch`, `dns.resolve`) · Brain (agentischer Loop, native
tool_calls) · LLM-Adapter (freellm, Streaming) · Binary (`run`/`chat`) ·
**Out-of-band-HITL gegen ntfy.sh + iPhone** · **End-to-End `chat` mit echtem LLM,
gestreamt**.

**Offen / als Nächstes:**
1. **TUI** (charm.land / bubbletea, lipgloss, glamour) — im `cmd/`, berührt die
   TCB nicht; Streaming ins UI.
2. **Secure-by-default `chat`** — `net.fetch = Ask` + Handy-Freigabe als Default.
3. **Weitere Capabilities** (`ping`, Mail, Kalender) — Muster: kleiner Typ + `*Guard`.
4. **Skill-Schicht** (Extism vs. eigener Host + Javy) — geparkte Weiche, beide gespiket.
5. **Verteilung** (IronHub-Stil + Code-Signing), **Epoch ins Brain** (task-scoped
   Grants via „für-diese-Session-merken"), **tool_call_id**-Verfeinerung.

**Arbeitsweise:** Zwiebelschalig, ein Aspekt klären → bauen → als stabil beweisen.
Explizit statt implizit. Kein Wildwuchs. Kein Cruft.
