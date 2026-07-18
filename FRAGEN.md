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

---

### 12. Multi-Workspace: N-Stack pro Workspace + HKDF-Master-Vault

**Heute:** EIN Workspace, `wsDir` hart `"workspaces/default"` in app.go. Alles (Vault, Grants, Agents, Plugins,
Skills, filecap-Root) liest schon **aus `wsDir`** → die Mechanik ist per-Workspace **drop-in-ready**, es fehlt nur
Auswahl/Komposition. Ein Workspace = die **Isolations- und Portabilitäts-Einheit** (ADR-10): getrennte
Kontexte/Accounts (work/privat), je eigener Vault + Grants + Cage + Plugins.

**Zwei Geschmacksrichtungen (verschieden teuer):**
- **Einer zur Zeit (billig):** `wsDir` parametrisieren (`nocturn <name>`, Default `default`, oder ein Picker vor
  der TUI). Wechsel = Neustart. Kein neuer Sicherheits-Code — nur die Auswahl. **Erster Schritt.**
- **Gleichzeitig / multi-tenant (teuer, dieser Eintrag):** N volle Stacks parallel — je eigener
  `Guard`/`Registry`/`Injector`/`Grants`/`Scheduler`/`EpochRegistry`. Nötig, sobald Agents aus *mehreren*
  Workspaces unbeaufsichtigt laufen sollen.

**Wo die Isolation herkommt (Kern-Erkenntnis):** NICHT aus getrennten Vault-Keys, sondern aus dem
**per-Workspace `Injector`/`Guard`/`Cage`** — der Injector gibt A-Effekten nur A's Credentials (host+plugin-scoped),
ein A-Agent kann B's Token strukturell nicht ziehen, egal wie die Vaults verschlüsselt sind. Diese Laufzeit-Trennung
**steht schon** (per-WS-Ownership + ctx-Scoping). Ein Masterkey berührt sie nicht.

**Der Vault-Teil — ein Master, RICHTIG gemacht:** nicht „ein Key verschlüsselt alle Blobs", sondern
**ein Master → per-Workspace-Keys via HKDF, domain-separiert mit dem WS-Namen:**
`key_ws = HKDF(master, info="workspace:"+name)`.
- **Eine Entsperrung → alle Vaults auf** (löst multi-WS-unattended: kein N-Passphrasen-Problem).
- **Auf der Platte trotzdem kryptografisch verschiedene Keys** pro Vault, keine Wiederverwendung, Domain-Separation
  → ein geklonter einzelner Vault verrät nichts über die anderen.
- Das ist **IronClaws Vault-Muster** (per-secret HKDF-SHA256 + domain-separated AAD) — von CLAUDE.md als
  Table-Stakes markiert. Der Master kommt aus **einmal-Passphrase** (Daemon-Start) oder **OS-Keychain**
  (login-entsperrt, kein Prompt → echt-unattended). ⇒ koppelt an das offene **Keychain-Backend** (M4).

**Die ehrliche Einschränkung — Prozess vs. Speicher:**
- **Ein Prozess (logische Trennung, Default-Empfehlung):** alle Workspaces im selben Binary → nach dem Entsperren
  liegen alle Klartext-Secrets im selben Adressraum. Trennung ist **logisch** (Injector-Scoping), nicht
  speicher-hart. Plugins sind WASM-gesandboxt (kommen nicht dran); ein **Host-Bug** könnte theoretisch quer-lecken.
  Für unser Threat-Model (Plugins isoliert, Host = kleine auditierte TCB) vertretbar.
- **Prozess-pro-Workspace (speicher-hart):** N Binaries + IPC. Viel schwerer, selten nötig. Der HKDF-Master
  funktioniert in beiden Fällen.
- **Master = ein at-rest-Kompromiss-Punkt:** leakt er, öffnen alle Vaults. Nicht schlimmer als jedes
  Single-Unlock-System; die Laufzeit-Isolation hält weiter.

**Baureihenfolge:**
1. **Auswahl „einer zur Zeit"** — `wsDir` parametrisieren. Klein, sofort nützlich.
2. **HKDF-Master-Vault** — `secret`-Vault: aus einem Master per-WS-age-Identität ableiten (statt Passphrase pro
   Vault). Einzeln testbar, noch ohne Multi-Stack.
3. **N-Stack-Komposition** — app.go: pro entsperrtem Workspace einen Stack spawnen (eigener Guard/Registry/…),
   ein Scheduler je WS (oder ein Scheduler, der WS-getaggte Jobs feuert). Setzt Daemon-Modus voraus (die TUI
   bewohnt EINEN WS; multi-tenant ist ein Daemon-Bild).
4. **Keychain-Backend** für den Master → echt-unattended (M4).

**Kopplung:** multi-WS-unattended = Daemon-Modus **+** Keychain **+** HKDF-Master. Einzel-WS-unattended geht schon
(eine Entsperrung beim Start). Reload-Atomizität je WS s. #9.5.

---

### 13. `notify` — proaktiv den User erreichen (✅ GEBAUT)

**Status:** umgesetzt als `internal/notifycap` (Tool `notify` + `nocturn.notify`), ntfy-`Push`
(fire-and-forget), Leak-Scan + Rate-Limit, TUI-Fallback ohne ntfy. Race-clean getestet, docs
aktualisiert. Design unten wie geplant umgesetzt.


- **Ziel:** Der Assistent meldet sich **von sich aus** (fire-and-forget), im Gegensatz zu HITL
  (das *fragt und wartet*). „Flug verspätet", „Task fertig", „Cron-Agent hat X gefunden". Die
  fehlende „andere Hälfte" von HITL — ein Assistent, der dich nicht erreichen kann, ist ein halber.

- **Form:** neue Host-Capability-Gruppe `internal/notifycap` (kleiner Typ + `*Guard` + ntfy-Push +
  `secret.Scanner`). Familie `notify`. Tool **`notify`** `{title?, message}` → `{sent:true}`. JS:
  `nocturn.notify(message, title?)`. Kommt Modell/Skript/Plugin/Cron-Agent gleichzeitig zugute.

- **Ziel-Kanal host-owned, NIE modell-gewählt:** die Nachricht geht an den **konfigurierten**
  User-Kanal (ntfy-Topic aus `NTFY_*`), das Modell liefert nur *Inhalt*, nie *Ziel* — exakt wie die
  Credential-Injektion das Ziel bestimmt, nicht der Gast. Damit kann `notify` **kein Exfil-Kanal zu
  Dritten** werden (der Klassiker „Agent pusht Secret an vom Angreifer beobachtetes Topic" ist zu).

- **Gating (die Sicherheits-Balance):**
  - **`Write:false`** — eine Nachricht an dein *eigenes* Gerät ist keine per-Message-freigabe-
    pflichtige Mutation → Base-Policy **Allow** → läuft still (keine „darf ich dir X sagen?"-Prompts,
    gute UX). Eine Workspace-/Agent-Policy kann trotzdem auf `Ask` verschärfen.
  - **Leak-Scan (Egress):** `Scanner.ScanEgress(title, message)` → ein Vault-Secret in der Nachricht
    wird **geblockt**. Das ist die eigentliche Kontrolle (Injection kann kein Secret rausschmuggeln).
  - **Rate-Limit** (`RateLimiter`, Familie `notify`) → keine Spam-Salve aufs Handy.
  - läuft durch den `Guard` → observable, cage-/policy-verschärfbar.

- **Transport:** ntfy um ein schlichtes `Push(title, body)` erweitern (keine Action-Buttons, kein
  Token — anders als das HITL-`Notify`). Reuse `NTFY_*`-Config. Kein ntfy konfiguriert → attended:
  TUI-Zeile; unattended: no-op + Log (kanal-agnostisch, s. #7).

- **Baugröße:** klein. **Zuerst bauen** (fundamentaler als #14; ein Reminder = #14 + #13).

---

### 14. `schedule` — künftige/wiederkehrende Läufe (PLAN, noch nicht gebaut)

- **Ziel:** Autonomie über Zeit. „Erinnere mich morgen 9 Uhr", „prüf X jeden Morgen". Der
  **Scheduler existiert schon** (`internal/agent/scheduler.go`: cron-getriggerte, *autor-deklarierte*
  `Definition`s aus `<ws>/agents/<name>/agent.md`, unattended, mit Autonomy-Level). `schedule` ist die
  **dynamische, modell-sichtbare** Hälfte: der laufende Agent legt selbst Läufe an/listet/löscht.

- **DER Sicherheits-Kern (load-bearing):** ein Schedule erzeugt **künftige Autorität**. Könnte das
  Modell einen unattended-Lauf mit beliebiger Autonomy planen, könnte eine Prompt-Injection einen
  `full`-Lauf schedulen, der Effekte **auto-approved** → **HITL-Bypass über die Zukunft** (dieselbe
  Klasse wie `grants.json` schreiben / Agents authoren, ADR-10). Das ganze Design folgt aus: *das
  Modell darf sich keine künftige Autorität selbst verleihen.*

- **Was ein Schedule tut — nach Sicherheit gestaffelt:**
  - **(a) Reminder (MVP, sicher, hoher Wert):** `{when, message}` → zur Zeit feuert ein
    **`notify(message)`** (und/oder re-prompted den Assistenten mit der Message als neuen **attended**
    Turn, wenn du da bist). Autorität = nur `notify`. Eine Injection, die einen Reminder plant, kann
    dich höchstens später benachrichtigen (leak-gescannt) — **kein** unattended-Effekt. Der 80%-Fall.
  - **(b) benannten, vor-authorten Agent feuern (später):** `{when, agent}` mit `agent` = eine
    **existierende** autor-deklarierte `Definition` (human-reviewtes agent.md). Modell wählt *wann*;
    *was* + Tools/Cage/Autonomy kommen aus der Control-Plane-Datei, die das Modell **nicht** schreiben
    kann → keine neue Autorität geprägt, nur ein bereits sanktionierter Lauf getriggert.
  - **NIE:** Modell liefert Instructions + Tools + `full`-Autonomy inline → das wäre Agent-Authoring
    zur Laufzeit = der Self-Modification-Threat. Verboten.

- **Autonomy-Cap für modell-erzeugte Schedules:** höchstens `guarded` (out-of-band fragen → Mensch
  bleibt in der Schleife) oder `strict`; `full` **nur** via autor-deklariertes agent.md.

- **Form:** Tools `schedule.create {when, message | agent}`, `schedule.list`, `schedule.cancel {id}`.
  `when` = one-shot absolut (`at:` RFC3339 / relativ „in 2h"/„morgen 9 Uhr") **oder** recurring
  (`cron: "0 9 * * *"`). Braucht einen **One-shot-Schedule-Typ** (aktuell nur `ParseCron` = recurring)
  der nach dem Feuern sich selbst löscht. JS: `nocturn.schedule(...)`.

- **Persistenz (Control-Plane, modell-unerreichbar):** `<ws>/schedules.json`, host-managed, 0600,
  **außerhalb** des Modell-Mounts — erreichbar **nur** übers gegatete `schedule`-Tool, **nie** via
  `file.write` (dieselbe load-bearing Schutz-Logik wie `grants.json`, ADR-10). Scheduler lädt beim
  Start; das Tool appended/entfernt zur Laufzeit → Scheduler braucht **dynamisches `Add(job)`/
  `Remove(id)`** (heute statisch bei `NewScheduler`).

- **Gating:** `schedule.create` = `Write:true` (legt einen stehenden künftigen Effekt an) → Base
  `Ask` (HITL: „Reminder für morgen 9 Uhr anlegen?"); danach persistiert. `schedule.list` = read
  (still); `schedule.cancel` = write (ask/allow-own). Rate-limited.

- **Scheduler-Änderungen:** dynamisches Add/Remove (Mutex); One-shot-Jobs (self-delete nach fire);
  neuer Run-Typ „Reminder" (nur notify) neben dem bestehenden `Definition`-Run.

- **Offene Punkte:** re-entrant Reminder (Message als neuer Turn) vs. nur notify — MVP: nur notify;
  Zeitzone (Workspace/Host-lokal, koppelt an `time.now`-tz); Max-Pending-Quota pro Workspace.

- **Zusammenhang:** Reminder = **#14 + #13**. `notify` (#13) ist der kleinere, fundamentale Baustein
  → zuerst. Beide sind **Bindegewebe** für einen echten Agent (den User erreichen; von Zeit geweckt
  werden), nicht weitere Effekt-Tore.

### 15. Cross-Workspace-`spawn` — bewusstes Loch in die harte Isolation (PLAN, noch nicht gebaut)

**Frage (User):** Multi-Workspace-B isoliert Workspaces *strukturell* (jeder Stack eigener Guard/Injector/
Grants; #12). Was, wenn man das bewusst *will* — `default` soll an einen Agent von `work` routen?

**Antwort: harte Isolation ist der richtige Default (stoppt *ambientes* Quer-Leaken), aber keine
unöffenbare Mauer.** Gezieltes Quer-Routing = der **Subagent-/`spawn`-Weg, workspace-übergreifend** —
dieselbe Form wie Plugin-Cage→Hallway (strukturell dicht, ein **deklariertes, attenuiertes** Loch).

**Design (sicher, weil der geroutete Agent im ZIEL-Stack läuft):**
- Ein Agent in `default` darf `spawn work.<agent>` — **wenn eine Route deklariert + reviewt ist** (nicht
  ambient; `default` erreicht nur freigegebene Workspaces/Agents).
- Der Ziel-Agent läuft **in `work`s Stack** (`work`s Injector/Guard/Grants), `default` *triggert* nur und
  bekommt das Ergebnis als **untrusted Data** zurück.
- **Credential-Isolation hält:** `default` sieht `work`s Token nie (Injector stempelt an `work`s Grenze).
- **Effekt-Isolation hält:** ein Write des `work`-Agents geht durch **`work`s Guard + HITL**, getaggt
  `[work]` (der Workspace-Label-Seam aus #12-§5) — ein gekapertes `default` kann `work` **nicht still**
  senden lassen.
- **Schraube:** cross-workspace-erreichbare Agents **erzwingen ≥ `guarded`** (sonst könnte `default` einen
  `full`-Agent still treiben). Die Route-Deklaration ist der Review-Punkt.

**Kein Redesign:** map-of-stacks (eine Instanz) macht's zu einem **kontrollierten Entry-Point** (default's
Stack kriegt ein `spawn(work.<agent>)`-Tool, das in `work`s Stack dispatcht) — additiv. Verallgemeinerung
der geparkten Subagents (BB8, same-workspace) auf cross-workspace. **Erst wenn Subagents dran sind.**

---

### 16. Egress-Leak-Scan: Body als `[]byte` scannen statt kopieren (REFINE, noch zu klären)

**Kontext:** Der Egress-Scan (`gateway.Do` → `EgressScan`/`ScanEgress`) bekommt die outbound-Fläche als
`func() []string`. Das `[]string` selbst ist billig (Go-Strings = ptr+len-Referenzen, keine Kopien; der
Scanner walkt + short-circuited beim ersten Leak). **Aber:** in `netcap.egressParts` steht
`string(req.Body)` → die `[]byte→string`-Konvertierung **kopiert den Request-Body**, und
`percentDecode` im Scanner kopiert nochmal. Bei einem großen POST-/Upload-Body sind das reale transiente
Allokationen (~2× Body-Größe).

**Zu klären / refinen (nicht jetzt):**
- Scanner um einen **`[]byte`-Egress-Pfad** ergänzen (z. B. `ScanEgressBytes(...[]byte)` oder ein Egress-Part
  als `[]byte` statt `string`), sodass der Body **ohne Konvertierung** gescannt wird.
- `percentDecode` **überspringen**, wenn kein `%` im Input (spart die zweite Kopie im Normalfall).
- Offene Design-Frage: wie mischt man `[]byte`-Body + `string`-Parts (URL/Header) in *einer* Egress-Fläche,
  ohne die simple `func() []string`-Naht zu verbiegen? (Evtl. eine kleine Part-Union oder ein zweites
  optionales `bodies func() [][]byte`.)
- **Kein Walker/Iterator** statt `[]string` — der spart nur den winzigen Header-Slice (~nichts) und
  verkompliziert den `anyNonEmpty`-Empty-Check. Der Hebel ist **body-spezifisch**, nicht die API-Form.

**Status:** Mikro-Optimierung, erst relevant wenn große Uploads über `http.write`/Plugins laufen. Die
`func() []string`-Naht bleibt vorerst.

---

### 17. `remind` + `wake` — zwei getrennte Zeit-Mechanismen (✅ GEBAUT)

**Status:** umgesetzt als `internal/remindcap` (persistent, gegated, eigene `time.AfterFunc`-Timer,
`reminders.json` Control-Plane, `remind`/`.list`/`.cancel` + `nocturn.remind`) und `internal/wakecap`
(ephemeral, ungegated + geboundet, `wake` + `nocturn.wake`, TUI-Resume via `selfWakeMsg`). **NICHT** in
den `agent.Scheduler` gemischt (bewusst zurückgebaut — der feuert Agent-Läufe, Reminder feuern nur
notify). Timer-Tests mit `testing/synctest`. Design unten wie geplant. Offen bleibt: wake×User-Input-
Interrupt — **gelöst via Input-Buffer** (Type-ahead + `wake` queuen → bei Turn-Ende füttern, FIFO, sichtbar
im TUI; s. #18). Offen: Clamp war 60s (Claude-Code-Erbe) → auf **1s** gesenkt (lokaler Assistent: „in 2s"
ist legitim). remind-`cron`-wiederkehrend, Quota/TZ-Kanten.


Präzisiert #14. Beim Ausdiskutieren kam raus: „Schedule" meint in Wahrheit **zwei grundverschiedene
Dinge**, plus der schon existierende Cron-Agent. **Drei Feuer-Verhalten:**

1. **notify-Reminder** → kein Modell-Lauf, nur Push. → `remind` (unten).
2. **Cron-Agent** → frischer unattended `RunTask` (separate Session). → **existiert schon**
   (`internal/agent/scheduler.go`, autor-deklarierte `agent.md`-`Definition`s). Der dynamische,
   modell-erzeugte Task-Lauf (Autonomy-gekappt + HITL) ist der schwere Rest von #14.
3. **Self-Wake** → **dieselbe** Session mit Fortsetzungs-Prompt wieder invoken. → `wake` (unten).
   (Vorbild: Claude Code `ScheduleWakeup` im dynamischen `/loop`.)

|                | `remind`                                   | `wake`                                      |
|----------------|--------------------------------------------|---------------------------------------------|
| Feuern         | reines `notify` (kein Lauf)                | **dieselbe** Session setzt fort             |
| Kontext        | entkoppelt (Inhalt beim Anlegen erfasst)   | **erhalten** (gleiche Conversation)         |
| Lebensdauer    | **persistent** (übersteht Neustart)        | **ephemeral** (nur solange Prozess lebt)    |
| Autorität      | broker-gegated (still), leak-scan + rate   | **null extern** (Control-Flow), stattdessen *geboundet* |
| Zweck          | „erinnere mich morgen an X"                | self-paced Loop / Polling („in 5 min nochmal prüfen") |

#### `remind` — persistente Zukunfts-Erinnerung
- **Tools:** `remind {when, message}` → `{id, fireAt}`; `remind.list` (read, still); `remind.cancel {id}` (write).
- **`when`:** relativ (`"in 2h"`, `"morgen 09:00"`) | absolut (RFC3339) → host-seitig zu absolutem
  `fireAt` (Workspace/Host-TZ, koppelt an `time.now`). Optional später: `cron` = wiederkehrend.
- **Gating:** durch `Guard` (observable/cage-/policy-fähig), aber **`Write:false` → still** (ein benigner
  Zukunfts-Hinweis braucht keine per-Reminder-Freigabe). Kontrollen wie `notify`: **Leak-Scan beim
  Anlegen** (fail-fast) + **Rate-Limit** (Anti-Spam). Ziel-Kanal host-owned.
- **Persistenz:** `<ws>/reminders.json` — **Control-Plane, 0600, AUSSERHALB `mnt/`** → Modell kann's
  weder sehen noch `file.write`en; einziger Weg = das gegatete Tool (load-bearing wie `grants.json`,
  ADR-10). Übersteht Neustart; per-Workspace portabel.
- **Feuern:** Scheduler-One-shot zur `fireAt` → `notify`-Pusher (Message **nochmal** leak-gescannt) →
  Eintrag löschen (one-shot).
- **Layering:** `remindcap` (klein) = `*Guard` + Reminder-Store + `notify`-Pusher; `notifycap` bleibt
  reiner Transport; der Store + One-shot-Job kommen in die Scheduler-Schicht.

#### `wake` — Self-Wake / Resume derselben Session
- **Tool:** `wake {seconds, note}` (oder `wait`): der aktuelle Turn **endet** (Modell hört auf), der
  **in-Prozess-Scheduler** ruft nach `seconds` **`session.Ask(note)` auf DERSELBEN `agent.Session`**
  (Conversation lebt weiter). Fortsetzung, **keine** Injection in einen laufenden Turn (der hat geyieldet).
- **Autorität:** **nicht broker-gegated pro Call** — erreicht nichts Externes, reine Control-Flow-Planung
  (wie `time.now`, null Autorität). Die Effekte im **wieder-aufgewachten** Turn treffen normal Broker + HITL.
- **Bounds statt HITL (gegen Runaway):** Delay clampen (z. B. `[60s, 1h]` wie Claude Code); **Max-Wakes /
  Budget-Cap** (koppelt an `internal/deadline`); ein Self-Wake-Loop teilt EIN Budget → weckt sich nicht endlos.
- **Ephemeral:** lebt nur solange TUI/Daemon läuft; **nicht** persistiert. Prozess tot → pending Wakes weg.
- **Session-Kopplung:** an die Epoche der Session gebunden — `Reset`/`Close` (Epoche schließt) **verwirft
  pending Wakes** (alte Conversation/Autorität ist tot). Sauber by construction.

#### Gemeinsame Scheduler-Arbeit (Voraussetzung für beide)
`agent.Scheduler` kann heute nur cron-`Definition`s statisch bei `NewScheduler`. Beide brauchen:
- **One-shot `at:`-Jobs** (feuern einmal zur absoluten Zeit, self-delete danach),
- **dynamisches `Add(job)`/`Remove(id)`** zur Laufzeit (Mutex),
- zwei Job-Arten: **notify-only** (remind) und **resume-session** (wake) neben dem bestehenden **fresh-RunTask** (cron).

#### Offene Punkte
- **wake × User-Input:** tippt der User, während ein Wake pending ist — Wake danach anhängen, oder
  (wie Claude Code) User-Input **interrupted** den Loop? Interrupt-Semantik klären.
- **remind:** Max-Pending-Quota pro Workspace; TZ-Kanten (DST); `cron`-wiederkehrend ja/nein im MVP.
- **wake:** Max-Total-Wall-Clock / Max-Iterationen; Verhalten bei `/ws`-Switch.
- **Baureihenfolge:** Scheduler-One-shot/Dynamik zuerst (gemeinsam), dann `remind` (persistent, gegated),
  dann `wake` (ephemeral, geboundet). `remind` ist näher an dem, was bisher steht (notify + Scheduler);
  `wake` braucht den Session-Resume-Pfad im `agent`-Paket.

---

### 18. Turn-Orchestrierung (Buffer/Run-Loop) gehört in einen App-Kern, nicht ins TUI (GELÖST)

**Gelöst:** Genau so gebaut — `session.Runner` (im Paket `session`) ist der headless Kern:
Commands rein (`Submit`/`SubmitInput`/`SubmitAgent`/`Cancel`/`Reset`/`Resolve`), Event-Stream
raus (`Subscribe`/`Snapshot`), besitzt Run-Loop + Queue + Sink-Stempelung + Approval-Routing.
`wake` ruft direkt `runner.Submit(SourceWake, note)` in **dieselbe** Queue → `selfWakeMsg` weg.
Die TUI ist ein **dünner Adapter** (`bindWorkspace` abonniert, `handleRunnerEvent` rendert);
Rendering/Textarea/Drafts bleiben client-lokal. Bereit für einen 2. Client (REST/WS, #9) ohne
weitere Orchestrierung. Race-clean getestet + echter TUI-Lauf verifiziert.

<details><summary>Ursprüngliche Richtungsentscheidung (Archiv)</summary>

**Kontext:** Der Input-Buffer (Type-ahead während eines Turns + `wake`-Note queuen → bei Turn-Ende
füttern) sitzt aktuell im bubbletea-`chatModel` (`cmd/nocturn/tui.go`: `m.queue`, `runQueued`, drain in
`doneMsg`, `wake` via `selfWakeMsg`). Das ist ein **MVP für die Ein-Client-Welt**.

**Entscheidung/Richtung:** Sobald weitere Clients kommen (REST, WebSocket, Tauri-App, Daemon), gehört die
**Turn-Orchestrierung in einen headless App-/Session-Kern, der Events emittiert** — TUI/REST/WS/Tauri werden
dünne Adapter. **Nicht** in bubbletea verankern.

**Warum:**
- **Gleiche Semantik für alle Clients:** „ein Turn zur Zeit + Buffer + drain-on-done" darf nicht pro Client
  reimplementiert werden (Drift/Bugs).
- **Headless/Daemon hat kein TUI:** ein unattended Lauf / WS-Client ohne Terminal braucht trotzdem
  Serialisierung + Puffern (eine `wake`-Note, die während eines Turns feuert, muss *irgendwo* queuen).
- **Die Queue ist Domain-State, nicht View-State:** sie bestimmt, *was als nächstes läuft* → jeder Client
  sollte sie sehen/steuern (queued Item canceln, Reihenfolge).
- **`wake` zeigt den Smell schon:** heute TUI-Umweg `selfWakeMsg` → `startTurn`; im Kern würde `wake` direkt
  `core.Submit(note, fromWake)` rufen, `selfWakeMsg` verschwände.

**Form (Ports & Adapters):**
- **Kern (pro Session/Workspace):** Commands rein (`Submit`/`Cancel`/`Interrupt`/`NewSession`/`SwitchWorkspace`)
  + Event-Stream raus (`token`/`toolStart-End`/`turnStart-End`/`queued`/`approvalNeeded`/`notice`); besitzt
  Run-Loop + Queue; `wake`/`remind` verdrahten in *dieselbe* Queue.
- **Clients = Adapter:** übersetzen Transport ↔ Commands/Events. Das heutige `send func(tea.Msg)`-Closure IST
  schon ein primitiver Event-Sink → zu einem typisierten Event-Port generalisieren = der Refactor. `agent.Session`
  (Lifecycle-Owner) ist der natürliche Ort; heute ist `Ask` synchron → Kern macht daraus eine async
  Command/Event-Fassade.
- **Client-lokal bleibt:** Rendering/Markdown/Scroll/Textarea/Keybindings + **Drafts** (getippt, noch nicht
  committed). Die *committed* Queue ist der Kern; zwei Clients an EINER Session teilen EINE Queue.

**Status:** Richtung festgelegt, **nicht jetzt bauen**. Extraktion fällig mit dem 2. Client — koppelt an #9
(REST/WebGUI-Zukunft). Bis dahin: keine *weitere* Orchestrierung ins TUI stapeln.

</details>

---

### 19. Rate-Limit ist nirgends verdrahtet — `Guard.Rate == nil` überall (GELÖST)

**Gelöst:** (1) `capability.RateLimiter` ist jetzt **per-Family** (`WithLimit(family, n, window)`;
unkonfigurierte Family = unlimitiert → reads bursten frei). (2) Rate zog **aus `Policy.Evaluate`
in den Gateway** (`Guard.rateCheck`) — der Broker bleibt pur (kein stateful I/O), der Gateway rate-
checkt **alle** autorisierten Pfade inkl. Base-`Allow`. (3) `workspace.Open` hängt einen Limiter an
den Guard (`notify` 10/min, `remind` 20/min). (4) **Bonus:** eine Rate-Ablehnung ist ein
`gateway.RateLimitedError{Family, RetryAfter}` (unwrapt zu `ErrDenied`) → das Modell erfährt **wann
es wieder geht** und kann `wake` nutzen oder dem Nutzer Bescheid geben. `WithClock` fiel weg
(synctest, go.dev/blog/testing-time). Race-clean getestet.

<details><summary>Ursprünglicher Fund (Archiv)</summary>

**Fund:** Das Sliding-Window-Primitive `capability.RateLimiter` (`Allow(family)`) existiert, aber
**`Guard.Rate` ist in JEDEM Workspace-Guard `nil`** (stack.go setzt es nicht). Folge: **heute wird
nichts rate-limitiert.** Der „Rate-Cap", den `gateway.Authorize` in den Auto-Pfaden konsultiert
(stehender Grant / autonomy-full), ist damit ein **No-op**.

**Regression diese Session:** `notify` hatte einen hand-gerollten `WithRate` — der wurde entfernt
(korrekt: hand-verdrahtet im Callback = Anti-Pattern, wie beim Leak-Scan), aber **ohne Ersatz** →
`notify` hat aktuell **null** Anti-Spam-Kontrolle. Gleiches gälte für ein künftiges `remind`-Spam.

**Design (wie besprochen):** Rate gehört in die gegatete Pipeline des `Guard`, konsultiert auf
**allen** autorisierten Pfaden — **inkl. Base-`Allow`** (heute prüft `Authorize` Rate NUR auf dem
Grant- und autonomy-full-Pfad, nicht auf `Allow` → deshalb musste notify es im Callback machen).
Per-Family.

**Haken:** `capability.RateLimiter` ist *ein* Limit/Window über alle Families (Buckets pro Family,
gleiche Config). „10/min für notify, unlimitiert für reads" braucht **per-Family-Limits** (kleine
Erweiterung: Map `family → limiter`, oder ein „nur diese Families raten"-Set) — sonst raten wir auch
`http.read`/`file.read` mit, was bursty Workloads (ein Skript mit 100 file.reads) bricht.

**Schließen =** (1) `Authorize` raten auf dem `Allow`-Pfad (per-Family), (2) einen Limiter in den
Workspace-`Guard` hängen (stack.go), (3) notify/remind eine sinnvolle Rate geben. Bringt notifys
Spam-Schutz **uniform** zurück und macht den bestehenden Grant/Autonomy-Rate-Cap überhaupt erst scharf.
**Priorität:** eher hoch — es ist ein **fehlender Control**, kein Nice-to-have.

</details>

---

### 20. Weitere Live-Sync-Domänen: `agents` + `settings` (PLAN, noch nicht gebaut)

**Kontext:** Der coarse Live-Sync-Bus steht (`appserver.Sync{Domain,WS,Activity}`, ein `syncHub`,
`WatchSync`) und trägt schon **chats** + **reminders**. Eine neue Domäne = **3 Touchpoints**
(Konstante+`Workspaces.X(ws)`; Manager/Service-`OnChange`→`emitList(DomainX,ws)`; Server-`case
DomainX`+`encodeX`) — kein neuer Hub, kein neuer Stream, kein Client-Protokoll-Umbau.

**Offen — was als eigene Live-Domäne app-seitig:**
- **`agents`** — heute liefert `Workspaces.Get()` nur eine STATISCHE `AgentInfo[]` (Name/Description)
  im `WorkspaceState`. Fehlt: eine LIVE `DomainAgents` mit **Run-Status/-Historie**. Frage: reicht die
  bestehende `origin=agent`-Chatliste (Cron-Runs sind schon Chats, live gepusht + filterbar), oder
  braucht's ein eigenes `agents{ws,items}` mit Schedule/letzter-Run/„läuft-gerade"? Tendenz: die
  Agent-*Deklaration* (statisch) + der Run als origin=agent-Chat reichen vermutlich — nur der
  „nächste Cron-Termin / läuft gerade"-Status wäre neu.
- **`settings`** — was ist app-seitig editierbar/anzeigbar? `persona` (schon: `setPersona`),
  `plugins`/`accounts` (heute presence, read-only). `DomainSettings` würde die Workspace-Detail
  (`WorkspaceState`) **live pushen** bei Änderung (statt nur Reply auf `getWorkspace`/`setPersona`).

**Offen ist der Scope/Payload pro Domäne, NICHT die Mechanik** — das Muster ist bewiesen (chats,
reminders). Priorität: nach dem Beta-Kern (Push/Pairing); pull-basiert (`getWorkspace`) tut's bis dahin.

---

### 21. Filesystem-Hot-Reload des Workspace-Control-Plane (PLAN, noch nicht gebaut)

**Kontext:** Der Workspace lädt seinen Control-Plane **beim Boot**: `agent.Discover(agents/)`,
`skill.Discover(skills/)`, Plugins (`plugin.json`), `PERSONA.md`, Reminder-Store. Editiert man eine
`agent.md`, droppt einen Skill, oder ändert `PERSONA.md` **auf der Platte**, greift das erst beim
**Neustart**.

**Idee:** einen FS-Watcher auf den Control-Plane (`agents/`, `skills/`, `PERSONA.md`, `plugins/`),
bei Änderung **re-discover** + **live an die App pushen** (via die Domänen aus #20 — `agents`/
`settings`).

**Haken (Sicherheit, load-bearing):** Ein Reload von `agent.md`/`plugin.json` ändert **Rechte**
(Policy/Cage/Autonomy/Tools). Ein hot-reloaded Plugin/Agent mit **erweiterter** Cage darf NICHT still
mehr Rechte bekommen — muss durch denselben Review-Gate (`approval`-Record, „schon reviewed") wie die
Erstinstallation. → **Zweiklassig behandeln:** `PERSONA.md` + Skills = harmloser Kontext (frei
hot-reloadbar); `agent.md` + `plugin.json` = rechte-tragend → Reload nur nach Re-Review, sonst bei
Erweiterung fail-closed.

**Deps:** `fsnotify` (neue Dep — TCB prüfen: pure-Go? klein?) vs. Polling (kein Dep, aber lag/CPU).

**Priorität:** DevEx-Nice-to-have, **nach** dem Beta-Kern. Voraussetzung, um Reloads app-sichtbar zu
machen, sind die Live-Domänen aus #20.
