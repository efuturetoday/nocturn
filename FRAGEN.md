# Offene Fragen

Lebende Liste offener Fragen & Entscheidungen, die wir noch klären wollen.
Erledigte wandern nach **Geklärt** (mit kurzer Antwort) oder raus.

---

## Offen

- **TODO (nicht jetzt bauen) — Weg B: async Skript-Gate für echte Parallelität in Skripten.**
  `nocturn.call` non-blocking machen (`submit(tool,args)→handle` + `await(handle)` / echtes
  JS-Promise), N Effekte Go-seitig nebenläufig ausführen, sodass `Promise.all([...])` im Skript
  echt parallelisiert. Umgeht das wazero-Limit (Go kann einen suspendierten Gast nicht async
  aufwecken; ein Host-Call blockiert die ganze Instanz) via **kooperativem submit/await** — die
  Nebenläufigkeit passiert im Go-Host, der single-threaded Gast awaited nur; nutzt den bestehenden
  Job-Pump (`JS_ExecutePendingJob`). Erst wenn ein Workload viele I/O-Effekte im Skript fächert.
  Brain-Level-Parallelität (Schale 2, s. CLAUDE.md §11) ist der kleinere, frühere Schritt.

### 1. Tool-Timeout pro Tool konfigurierbar?
- **Aktuell:** *globaler* `brain.Brain.ToolTimeout` (20 s) — uniform für ALLE Tools;
  zusätzlich HTTP-Client-Timeout 15 s im Gateway.
- **Frage:** Soll der Timeout **pro Tool** einstellbar sein? Ein `net.fetch` will
  ~15 s, ein `dns.resolve` ~5 s, ein späteres `exec`/`build`/`ffmpeg` evtl. Minuten.
  → z. B. Feld `ToolSpec.Timeout time.Duration` (0 = Brain-Default)?
- **Und:** Wie macht es die **Konkurrenz**? — recherchieren:
  - OpenClaw / IronClaw (per-tool `capabilities.json`? Timeout-Feld?)
  - MCP (Timeout im Protokoll? client-seitig?)
  - OpenAI/Anthropic tool-use (haben die überhaupt Tool-Timeouts, oder nur
    Request-Timeouts?)
- **Tendenz:** per-Tool-Override auf `ToolSpec` + Brain-Default als Fallback wirkt
  sauber und passt zum „Capabilities exportieren ihre eigene Spec"-Muster.
- **Notiz (User):** vorerst global lassen.

### 2. Tool-Ergebnis in der TUI sichtbar machen (Fehler/Cancel/Timeout → rot)
- **Problem:** Die TUI kennt nur `OnToolCall` (Tool *startet*), nicht das *Ergebnis*.
  Bei Fehler/Timeout/Cancel bleibt der Dot cyan, **die Fehlermeldung ist unsichtbar**
  (sie geht nur ans Modell zurück).
- **Lösung (skizziert):** `brain.Brain.OnToolResult(tc, out, err)`-Callback; in der
  TUI bei `err != nil` → Dot **rot** + Fehlerzeile darunter (Timeout → „timeout",
  Cancel → „abgebrochen").

### 3. Markdown wird nicht gerendert?
- **Beobachtung:** Antwort erscheint als roher Text, nicht als gerendertes Markdown
  (Glamour).
- **Zu klären:**
  - Rendert Glamour nur bei `done` (nach dem Stream) — greift das überhaupt? Renderer
    korrekt initialisiert (Width/AutoStyle)?
  - Gibt das Modell überhaupt Markdown aus?
  - Sichtbar war auch: das Modell **narriert** Tool-Calls als Text
    (`call net.fetch({...})`) *zusätzlich* zum `⚙`-Indicator → doppelt/verwirrend.
    Unterdrücken? (Systemprompt „nicht ankündigen" / eigenen assistant-„call …"-Text
    nicht in die History der TUI, nur den ⚙-Indicator.)

### 4. Skill-/Workspace-Scripts per Pfad ausführen (`code.run{path}`), statt sie durchs Modell zu schleifen

- **Hintergrund / Problem:** Ein Skill kann ein Helfer-Script bündeln (`.skills/<name>/scripts/x.js`).
  Um es heute laufen zu lassen, muss das Modell:
  1. `skill.read` → die **Script-Source landet im Modell-Kontext** (Tokens),
  2. daraus `code.run{source}` bauen → die **Source geht nochmal durchs Modell** (Tokens),
  3. dabei kann das Modell den Code **verhunzen** (Escaping, Auslassungen).
  Also: doppelter Token-Verbrauch + Korrektheitsrisiko, nur um ein Script auszuführen, das der Host
  ohnehin auf der Platte hat.
- **Idee:** `code.run` nimmt **`path` statt (oder XOR) `source`** (+ optional `input`). Der Host lädt
  die Source **von der Platte** und führt sie aus → **die Source erreicht das Modell nie. Null Tokens,
  keine Modell-Komposition.**
- **Boundary (offene Entscheidung):** zwei read-only Wurzeln, beide confined + QuickJS-sandboxed +
  Effekte weiter gegated:
  1. **`mnt/`** — Workspace-Scripts (der Data-Plane, den das Modell ohnehin sieht),
  2. **`.skills/<GELADENER-skill>/`** — Skill-gebündelte Scripts (host-managed, install-reviewed).
  *Nur `mnt` würde Skill-Scripts nichts bringen* (die liegen außerhalb mnt in `.skills`) → für das
  eigentliche Motiv (Skill-Scripts) müssen die Dirs *geladener* Skills mit rein. Frage: beide Wurzeln
  oder strikt nur mnt?
- **Wichtige Klarstellung (kam beim Ausdiskutieren):** Das **Script muss sich NICHT „ändern"** — der
  Mechanismus ist unabhängig davon. Der Umweg entstand nur, weil das erste Beispiel (`analyze.js`) als
  **Library** geschrieben war (definiert `analyze(text)`, tut von selbst nichts) → dann braucht es
  jemanden, der `print(analyze(...))` dranhängt. Ein **lauffähiges Programm** (macht beim Ausführen sein
  Ding, liest seinen Input, `print`t das Ergebnis — wie jedes CLI-Script `argv`/stdin liest) läuft mit
  `code.run{path}` direkt. Also: keine aufgezwungene Konvention; „Library vs. Programm" ist die Wahl des
  Script-Autors.
- **Input (offene Entscheidung):** ein parametrisiertes Script braucht den User-Input. Zwei Wege:
  (a) `input`-JSON → Host injiziert `globalThis.INPUT = …` davor, Script liest `INPUT`; oder
  (b) Host ruft eine deklarierte Entry-Funktion (`code.run{path, entry:"analyze", input:…}` →
  `…source…; print(JSON.stringify(analyze(INPUT)))`). (a) ist simpler, (b) lässt Library-Scripts unverändert.
- **Layering:** `code.run`/`script.Runner` bleibt **generisch** — es kriegt einen injizierten
  `SourceLoader(path) → (src, err)`, der mnt + geladene Skills auflöst. Die Boundary-Logik liegt in der
  Wiring-Schicht (`cmd`/`skill`/`filecap`), nicht im Runner.
- **Sicherheit:** unverändert — egal ob Source inline oder per Pfad geladen, sie läuft im QuickJS-Sandbox,
  jeder Effekt via `nocturn.call` → Broker + HITL. Pfad-Confinement (symlink-aufgelöst) wie `filecap`/`skill.read`.
- **Status:** nur festgehalten, **noch nicht umsetzen** (User).

### 5. Remote-MCP: bewusst NICHT gebaut (deny-by-default) + Folgearbeiten

Der Remote-MCP-Client (`internal/mcp` Protokoll + `internal/mcpcap` Gating, Spec-Revision
2025-11-25, tools über Streamable HTTP) implementiert absichtlich nur die Tools-Teilmenge.
Festgehalten, was fehlt und warum:

- **Sampling: VERWEIGERT — Sicherheitsentscheidung, keine Lücke.** Sampling hieße: ein
  *remote* Server schickt uns Prompts, die UNSER LLM mit UNSEREM Key ausführt — ein fremder
  Server im Fahrersitz unseres Brains, ideale Prompt-Injection-Rampe. Der Client deklariert
  deshalb `capabilities: {}` und überspringt strukturell JEDE server-initiierte Anfrage
  (SSE-Frames mit `method` werden verworfen). Nicht nachrüsten.
- **Resources / Prompts:** nicht implementiert — Tools-first (MVP); Ressourcen wären ein
  zweiter Ingress-Kanal für untrusted Content, erst mit eigenem Review-Konzept.
- **Elicitation / Roots / Completion:** nicht implementiert — alles server-initiierte bzw.
  FS-offenlegende Features; kollidiert mit Zero-Ambient-Authority (Roots) bzw. braucht
  eigenes UI (Elicitation).
- **OAuth-Discovery/DCR/Resource-Indicator nach Spec (RFC 9728/8414/7591/8707) — geplant,
  pausiert.** Heute-Stand: **kein Secret mehr in `mcp.json`/Env.** Zwei leak-freie Modi:
  `auth:"token"` → beim Setup no-echo-Prompt → verschlüsselter Vault; **manuelles** `oauth:{}`
  (Endpoints + client_id in der Config, `client_secret` folgt später via Vault). **Bearer ist
  auf dem Draht immer Pflicht** (Spec *Access Token Usage*); OAuth vs. Token = nur, woher das
  Bearer kommt. **Verifiziert:** GitHub-Remote-MCP (`api.githubcopilot.com/mcp/`) macht volle
  Spec-OAuth (PRM im `WWW-Authenticate` → AS `github.com/login/oauth`, RFC 8414, `S256`),
  **aber kein DCR** (`registration_endpoint` fehlt) → man muss eh eine OAuth-App vorregistrieren.
  **Der approved-aber-pausierte Ausbau** (Plan: `~/.claude/plans/immutable-conjuring-moon.md`):
  - `internal/mcp/oauth.go` **NEU** (stdlib, injizierter `*http.Client`, host-seitig ungegated —
    holt nur öffentliche Metadata): `DiscoverAuth` = 401-Probe → `WWW-Authenticate`.`resource_metadata`
    parsen (+ `scope`) **oder** Well-Known-Fallback `…/.well-known/oauth-protected-resource[/<pfad>]`
    → PRM (`authorization_servers[0]`, `scopes_supported`, `resource`) → AS-Metadata über die
    **Pflicht-Prioritätsliste** (`/.well-known/oauth-authorization-server/<pfad>` → OIDC-Varianten)
    → PKCE prüfen (`code_challenge_methods_supported` nichtleer, sonst **verweigern**). `RegisterClient`
    = RFC-7591-DCR (nur wenn `registration_endpoint`).
  - `internal/oauth`: `Authorize`/`NewCredential` **generalisieren** — `WithResource(uri)` (RFC 8707:
    `resource` auf Auth- **und** Token-Request **und Refresh**; kanonische URI = PRM.`resource`),
    Google-Spezifika (`access_type=offline`+`prompt=consent`) hinter `WithOfflineConsent()` (nur Google).
  - `mcpcap.Server` → `{name, url, auth∈{"","token","oauth"}, client_id?}`, `OAuthDecl` (Endpoints) entfällt;
    `wireMCPCredential(oauth)`: Discovery → client_id aus Config **→ sonst DCR → sonst Fehler**,
    `client_secret` no-echo→Vault → `Authorize(…WithResource)` → Vault.
  - **Client-Priorität** (Spec): pre-registered client_id → DCR → Prompt. **CIMD bewusst N/A**
    (lokales Single-Binary hat keine öffentliche HTTPS-URL zum Hosten eines Client-Metadata-Dokuments).
  - **Deferred bleibt:** Step-up bei `403 insufficient_scope` (Laufzeit-Re-Auth), Auswahl-UI bei
    mehreren `authorization_servers`.
- **SSE-Reconnect/Resumability:** `Last-Event-ID`/`retry`/GET-Stream nicht implementiert —
  ein abgerissener Stream ist ein Fehler (fail-closed), kein Auto-Retry. Ebenso kein
  GET-Listening-Stream (server-initiierte Nachrichten interessieren uns nicht, s.o.).
- **Session-Lifecycle-Reste:** HTTP 404 auf `Mcp-Session-Id` → Spec will Re-Initialize;
  wir geben den Fehler hoch. `DELETE` zum Session-Beenden (SHOULD) fehlt (Transport ist
  POST-only). `notifications/tools/list_changed` wird ignoriert — Tools werden einmal beim
  Start gelistet + registriert.
- **Version-Pragmatik:** Wir sprechen 2025-11-25, akzeptieren aber auch 2025-06-18 und
  2025-03-26 vom Server (unsere Teilmenge ist in allen identisch); alles andere bricht
  fail-closed ab.
- **Decline = Skip:** Ein beim Start-Review abgelehnter MCP-Server wird übersprungen (App
  läuft ohne ihn weiter) — anders als ein abgelehntes Plugin (Startabbruch). Angleichen?
- **Offline beim Start = Startabbruch (Bug, Sofort-Fix):** Scheitert `conn.Connect` mit einem
  **Netzwerk**fehler (kein `StatusError`), gibt `loadMCP` den Fehler hoch → die **ganze App startet
  nicht**. Ein flakiger Remote-Server darf den Assistenten/Agent-Run nie blockieren → offline soll
  **skippen + Notiz** (wie „decline = skip"), mit den vorhandenen Tools weiterlaufen. Klein, unabhängig machbar.
- **Tool-Schema-Cache + lazy connect (Resilienz für die Agent-Schicht):** bei erfolgreichem Connect das
  `tools/list` in `<ws>`-State cachen (wie `approved.json`) → ein offline-at-start-Server kann seine Tools
  **trotzdem registrieren** (der Agent „hat" `github.*`); die Verbindung wird **lazy** (erst beim ersten Call
  verbunden/reconnected), ein Call gegen einen offline Server → **sauberer Fehler** ans Modell (kein Crash),
  autonomer Run **retryt beim nächsten Tick**. Zusammen mit Auto-Re-Init (#… Session-Lifecycle) die MCP-Resilienz:
  „Tool *definiert*" ≠ „Server *jetzt* erreichbar".

### 6. Plugin-Uninstall: persistierte Credentials + Server-Revoke aufräumen

- **Aktuell (nach dem plugin-scoped-Injection-Fix):** `Host.Uninstall` entfernt Tools + Bindings, und
  `Injector.RemoveBindingsFor` droppt jetzt **auch die In-Memory-Source** (sicher, weil Credentials
  owner-namespaced sind: `plugin:<name>/<cred>`). Also: In-Memory-Credential wird beim Uninstall vergessen.
- **Noch offen (cmd-Ebene):**
  1. **Persistierte Token-Datei** `<config>/nocturn/oauth/<plugin>-<name>.json` beim Uninstall **löschen**
     (best-effort) — sonst bleibt der OAuth-Refresh-Token nach „Uninstall" auf der Platte.
  2. **Optional best-effort OAuth-Revoke** (Token server-seitig bei Google/Provider widerrufen) — sonst ist
     das Token beim Provider weiter gültig, bis es abläuft.
- **Blocker/Caveat:** es gibt **keine Runtime-Uninstall-Aktion** (kein „Plugin entfernen"-Command in der TUI);
  `Host.Uninstall` ist API-only. Der Datei-/Revoke-Teil hängt daher an einem künftigen „Plugins verwalten"-Flow
  (TUI-Command + `plugins.go`-Verdrahtung). Dann in einem Rutsch: Uninstall → Bindings/Source weg (steht) →
  Token-Datei löschen → optional revoke.
- **Status:** In-Memory-Teil erledigt; Datei/Revoke festgehalten, an Manage-Flow gekoppelt.

### 7. Channel-agnostische Credential-/Approval-Erfassung (TUI ist nur EIN Channel)

- **Kontext:** Die TUI ist nur *ein* Channel von Nocturn — eine **REST-Schnittstelle + Web-UI** ist geplant
  (bequemer für Skills-Katalog, Workspace-Management etc.). Jede terminal-gebundene Interaktion muss daher
  hinter einen **Port** (wie `hitl.Notifier`), damit die Web-UI dieselbe Logik über einen anderen Adapter bedient.
- **Heute sauber genug:** Der *Mechanismus* ist channel-agnostisch in `internal/` (`secret.Vault`, `mcpcap` —
  keine Prompts). Die terminal-I/O ist **auf `cmd/nocturn/` beschränkt** (`readPassphrase`/`term`, `askYesNo`,
  `reviewMCP`, `wireMCPCredential`, `reenterOnRejection`) → kein Terminal-Coupling im Kern.
- **Offen (mit REST/Web):**
  1. **Vault-Unlock** (`unlockVault`, `term.ReadPassword`) → Port „Passphrase-Provider" (Web-Login-Form, ggf.
     Session-gehaltener Vault statt Prompt-pro-Start).
  2. **Setup-Prompts** (Plugin-Install-Review, MCP-`Connect?`, Token-Eingabe, `reenterOnRejection`-Re-Auth) →
     channel-neutrale Orchestrierung mit injiziertem „Prompter"/„Approver"; `loadMCP`/`loadPlugins` sind derzeit
     TUI-Setup und wandern in eine Channel-übergreifende Schicht.
  3. **HITL** hat den Port schon (`hitl.Notifier` → ntfy) — Muster für die obigen übernehmen.
- **Merke:** Nichts Terminal-Only in `internal/` wachsen lassen; neue interaktive Schritte immer als Port +
  TUI-Adapter, damit der REST-Adapter bruchfrei danebentreten kann.

### 8. MCP-Credential host-gebunden (Same-Name/Other-URL-Exfil) — erledigt + Follow-ups

- **Erledigt:** Der Vault-Key des MCP-Bearers ist an `(name, host)` gebunden —
  **`mcp:<name>@<host>/oauth`** (`mcpcap.SecretName(name, host)`, host lowercased). Ändert man in
  `mcp.json` bei gleichem Servernamen die **URL/den Host**, ändert sich der Key → kein gespeicherter
  Wert → `wireMCPCredential` **fragt neu**, und der alte Token wird **nie** an den neuen Host injiziert
  (InjectMatching fail-closed). Gleiche URL = gleicher Key = Token überlebt Neustart. Exfil-Regressionstest
  `TestConn_HostRebind_NoCrossHostExfil`. Owner-Scoping (`mcp:<name>`) bleibt namensbasiert.
- **Offen (Follow-ups):**
  1. **Purge-on-removal (EIN Reconcile über 3 Dateien):** Ein entfernter Server/Plugin hinterlässt Waisen in
     **`secrets.age`** (Token), **`grants.json`** (Always-Grants) **und** **`approved.json`** (Review-Memo) —
     alle **harmlos** (kein Binding/Consultant referenziert sie; Loader lesen nur existierende Einträge).
     Der richtige Fix ist **ein** Reconcile-Pass am „Manage-Flow" (Workspace = Source of Truth, ADR-10), der
     abgeleiteten State für nicht mehr vorhandene Plugins/Server gebündelt prunt — bewusst „melden + explizit
     prunen", nicht still-aggressiv (temporär deaktiviert soll State behalten). Braucht u. a. eine
     **Vault-Key-Enumeration** (heute `store.snapshot()` unexported). Optional OAuth-Revoke beim Entfernen. (#6)
  2. **Plugin-Pendant — erledigt:** `plugin.SecretName(owner, cred, host)` → **`plugin:<name>/<cred>@<host>`**
     (install.go), OAuth-Token host-gekeyt (plugins.go `wirePluginOAuth`). Manifest-Host-Wechsel bei gleichem
     Plugin-/Credential-Namen → anderer Key → Re-Auth, kein stiller Cross-Host-Reuse. Zusätzlich erzwingt
     `Manifest.Validate()` jetzt, dass jeder `credential.host` vom `cage` gedeckt ist (Kohärenz).
     Regressionstest `TestHost_CredentialHostBound_NoCrossHostReuse`. (Plugins waren ohnehin besser umzäunt:
     Cage + Install-Review-bei-jedem-Start; jetzt zusätzlich strukturell host-gebunden.)
  3. **Granularität:** Bindung an Hostname (kein Port), konsistent mit dem `hostMatches`-Scope; Host:Port
     später optional, falls je nötig.
- **Approved-Record (erledigt):** `internal/approval` (`<ws>/approved.json`, control-plane, 0600, fail-safe).
  Unveränderte Plugins/MCP-Server installieren/verbinden **ohne Re-Prompt**, geänderte re-promoten **mit Diff**
  (Hash für Gleichheit, Deklaration für den Diff). **Nur Review-Memo, keine Autorität** (Broker/HITL/Cage
  unverändert; ≠ `grants.json`). Verdrahtet in `loadPlugins`/`loadMCP`; Diff via `printApprovalDiff`.

### 9. Runtime-Reload von Plugins/MCP + laufende Agenten (REST/WebGUI-Zukunft)

Heute: **Boot-only, EIN Workspace** (`workspaces/default` hart in app.go). `loadPlugins`/`loadMCP` verdrahten
**einmal** beim Start (stdin, vor der TUI); kein Laufzeit-Install/-Reload.

- **Primitive sind bereit:** `plugin.Host.Install/Uninstall`, `tool.Registry.Add/Remove/Has` (RWMutex),
  `secret.Injector.AddBinding/RemoveBindingsFor` (Mutex), Epoch/Grants — je **einzeln** concurrency-safe.
  Laufzeit-Add/Remove ist also mechanisch möglich.
- **Was für REST/WebGUI fehlt:**
  1. **Channel-agnostische Install/Uninstall/Reload-Operation** — die Orchestrierung (Dir walken, Review, OAuth)
     liegt heute in cmd-Startup; muss hinter einen **Port** (Prompter/Approver, #7), damit die WebGUI dieselbe
     Logik fährt statt sie zu duplizieren.
  2. **Reload-Atomizität:** ein Reload ist mehrstufig (Tools raus → Bindings raus → neue Tools/Bindings → OAuth).
     **Nicht atomar** → ein gleichzeitiger Tool-Call kann einen Halbzustand sehen → **transient fail-closed**
     (sauberer Fehler, **keine** Korruption). Fix: **per-Workspace-Reload-Lock**, der Tool-Invocation kurz
     quiesct → Reload atomar ggü. Calls.
  3. **Bricken laufende Agenten? Nein — graceful degrade:** ein bereits aufgelöster In-Flight-Call läuft zu Ende
     (Plugins **stateless**, frische Instanz pro Call); ein Call auf ein gerade entferntes Tool → „tool not
     found" → das Modell bekommt einen Fehler und macht weiter; neue Tools erscheinen **nächste Runde** (der
     brain-Loop liest `Specs` je Runde). **Kein Shared-State-Schaden.**
  4. **Modell-Konsistenz je Runde:** das Tool-Set kann sich zwischen Runden ändern; innerhalb einer Runde kann
     das Modell ein inzwischen entferntes Tool rufen (→ Fehler). Akzeptabel; optional Tool-Set je Runde snapshotten.
  5. **Pro-Workspace-Isolation:** Multi-Workspace = eigener Stack je Workspace (Registry/Injector/Guard/Grants);
     Reload in A berührt B nie. Setzt die **Workspace-Schicht** voraus (eigene id + Persistenz + Cage, CLAUDE §11).
- **Synergie:** Approved-Record + Host-Binding (#8) sind bereits die Enabler — ein geänderter Plugin re-promptet
  mit Diff, ein repointeter Credential erzwingt Re-Auth: genau die Sicherheits-Checks, die ein Live-Reload braucht.

### 10. Agent-Autonomie: Regler statt starres Envelope (festgehalten, NICHT jetzt)

**Kern-Einsicht:** Ein **attended** Agent-Run im `guarded`-Modus (Reads still, Writes fragen) **ist praktisch
eine Session** — der Mensch sitzt davor und beantwortet die Asks. Schale 1 liefert das schon. Ein „abgeleitetes
Envelope" ist dafür **überflüssig** → erst nötig, wenn's **autonom** läuft (cron/Trigger, KEIN Mensch da):
dann müssen **Reads still** sein (sonst pingt das Handy für einen Wetter-Lookup) und **Writes out-of-band
pending** (Budget pausiert beim Warten).

**Wie andere es machen (Recherche, Anhang):** Zapier/n8n = Account einmal verbinden, dann frei (Trust aus
Autorschaft, deterministische Schritte). Handy-Apps/iOS-Shortcuts = Rechte bei Setup, dann frei. Coding-Agents
(Claude Code/Cline/Goose) = **Modi/Allowlist**, eskalierbar bis Voll-Auto. Custom-GPT = per-Domain „allow
once/always/decline". **Gemeinsam:** Account-Connect + **Autonomie-Regler** (default sicher, hochdrehbar).
**Fast niemand** erzwingt Per-Effekt-Fragen im Autonomie-Modus.

**Nocturns Grund härter zu sein:** LLM-Agent ≠ deterministisch → **Prompt-Injection** (in gelesener Mail/Web)
kann ihn *innerhalb* verbundener Accounts kapern. Deshalb Writes gaten. **Aber** „jeder Write fragt, nicht
abschaltbar" ist zu grob.

**Geplant (wenn Autonomie gebaut wird):** ein **`autonomy`-Regler pro Agent** statt Zwang:
- `full` → wie Zapier, fragt nie (vertrauenswürdiger Agent / nur trusted Input).
- `guarded` (Default) → Reads still, Writes out-of-band (für Agents, die untrusted Input verarbeiten — dort ist
  Injection real).
- `strict` → fragt alles.
Das „Envelope" ist dann nur der **abgeleitete Default-Preset von `guarded`** (aus der `tools:`-Liste: Reads→auto,
Writes→ask; nativ/Plugin-Manifest/MCP-`readOnlyHint`), **kein Käfig**. UX-Bild: „Accounts verbinden, Tools
ankreuzen, Autonomie wählen" — wie eine App einrichten, nicht eine Capability-Matrix ausfüllen. Braucht: cron-
Scheduler + out-of-band-Pending (ntfy steht) + die Envelope-Ableitung. **Erst wenn autonome Agents dran sind.**

**Status-Update (gebaut):** Der `autonomy`-Regler existiert jetzt — `capability.Autonomy`
(Attended/Guarded/Strict/Full), im Guard verdrahtet (unbeaufsichtigter `Ask` → strict=deny · guarded=out-of-band ·
full=auto-allow; consequential-Floor + Cage/Deny unantastbar), Frontmatter `autonomy: guarded|strict|full`, plus
**cron-Scheduler** (`internal/agent/scheduler.go`, Overlap-Skip, injizierbare Uhr) im Binary. Offen bleibt die
**Envelope-Ableitung als UX-Preset** (aus `tools:`) und webhook/REST als zweiter Trigger.

---

### 11. Modell-Wahl pro Agent — `Definition.Model` ist geparst, aber NICHT verdrahtet

**Beobachtung (Notiz):** `Definition.Model` + Frontmatter `model:` existieren und werden geparst, aber `RunTask`
**ignoriert** das Feld — ein `model:` im Agent hat heute **keine Wirkung** (dieselbe „geparst-aber-ignoriert"-Falle
wie damals `allowed-tools` bei Skills). Der Brain hält *einen* `llm.Client` mit fixem `modelName`; es gibt auch
keinen TUI-Model-Selector.

**Warum sinnvoll:** verschiedene Agents wollen verschiedene Modelle — ein billiges/schnelles für `pulse`/Triage,
ein starkes für Recherche; und interaktiv evtl. ein Selector.

**Wie (sauber, weil `brain.Model` ein Port/Interface ist):** eine Model-**Factory** im `cmd` (hält `baseURL`/`apiKey`,
Modell variabel) an `RunTask` durchreichen; pro Lauf `sub := *b; sub.Model = factory(def.Model)` (Fallback Default,
`""` = Default). freellm nutzt `auto` → ein Override wäre schlicht ein anderer Model-String. Klein — kein
Broker/HITL berührt (Modellwahl ist keine Autorität).

**YAGNI:** erst wenn mehrere Modelle real gebraucht werden. Bis dahin: Feld dokumentiert lassen ODER die
Ignorier-Falle mit einem Kommentar an `Definition.Model` markieren, damit niemand `model:` setzt und sich wundert.
