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
- **OAuth Discovery/DCR (RFC 9728/8414/7591):** Spec-Clients MÜSSEN eigentlich Protected-
  Resource-Metadata-Discovery; wir machen bewusst **Config-based OAuth** (Endpoints +
  client_id in `mcp.json`, wie Plugin-Manifeste): Discovery hieße, URLs aus einer
  *untrusted* Server-Antwort zu fetchen und dorthin Tokens zu schicken — erst mit
  Validierungs-/Review-Konzept. `WWW-Authenticate`-Parsing + 401-Flow ebenso offen.
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
