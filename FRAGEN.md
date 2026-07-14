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
