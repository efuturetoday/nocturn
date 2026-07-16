# KONZEPT — Sicherheits-UX: das Zielbild & der Umbauplan

> Verhandeltes Zielbild für Nocturns Autorisierungs-Modell — benutzbar wie eine App,
> sicher gegen Prompt-Injection, ohne den Moat (verpflichtendes Out-of-band-HITL an
> Trust-Boundaries) zu opfern. Baut nur die vorhandenen Primitive um (Broker, Cage,
> Grants, Epoch, Budget, HITL, Intent) — kein Neubau des Kerns. Teil A = Konzept, Teil B =
> Implementierungsplan zum Absegnen. Code-Befunde gegen `internal/` verifiziert.

---

## TEIL A — DAS KONZEPT

## 1. Das Mental-Model (ein Satz)

> **„Ein Plugin darf bestimmte Hosts erreichen. Jedes seiner Tools ist entweder *lesend*
> (läuft still) oder *verändernd* (fragt nach) — und *kritische* Tools fragen immer.
> Einrichten ist wie eine App installieren, nicht wie eine Capability-Matrix ausfüllen."**

Drei Nutzer-sichtbare Ebenen, mehr braucht der Mensch nie:
1. **„Was könnte es je?"** → einmaliger Install-Review: erlaubte Hosts + Tool-Liste.
2. **„Was tut es ohne zu fragen?"** → lesend still, verändernd fragt (Default), pro Agent regelbar.
3. **„Was habe ich schon erlaubt?"** → einsehbare, widerrufbare Grant-Liste.

---

## 2. Die zwei Tore — die Wirbelsäule

Autorisierung ist **nicht eine** Frage, sondern **zwei** — mit zwei verschiedenen
Einheiten und zwei verschiedenen Trust-Eigenschaften. Der heutige Begriff „Capability"
presst beide in ein Wort; das ist die Wurzel der Verwirrung. Wir trennen sie:

| Tor | Frage | Einheit | Trust | Mensch? |
|---|---|---|---|---|
| **1 — Zaun** | Bleibt der *echte* Zugriff im erlaubten Rahmen? | **erreichter Host** (host-rekonstruiert) + *verändert es?* | **unfälschbar** (aus dem echten Request berechnet) | nein — still; außerhalb = hard deny |
| **2 — Freigabe** | Hat der Mensch *diesem Tool* zugestimmt? | **Tool × Host** | untrusted, aber benannt | ja — Policy + Grants + HITL |

**Warum diese Trennung sicher ist.** Der Zaun vertraut dem Tool-Namen kein bisschen — er
nimmt nur die host-berechneten Fakten (welcher Host wird *wirklich* erreicht, ist die
Operation *wirklich* verändernd). Ein Plugin, das heimlich `http POST evil.com` ruft,
wird am Zaun geblockt, weil der Host `evil.com` aus der echten URL parst und gegen die
Decke prüft — der Freigabe-Schritt wird nie erreicht. **Deshalb darf Tor 2 „weich"
(tool-benannt) sein: Tor 1 hat das echte Ziel schon eingezäunt.**

Ablauf (Reihenfolge im Guard bleibt wie gebaut, `gateway/gateway.go:62`):

```
Modell: github.issue_anlegen{repo:"acme/web"}
   │  Plugin-JS → nocturn.call("http", POST api.github.com/…)
   ▼
🚪 TOR 1  ZAUN  (still, unfälschbar)
   Host rechnet aus echtem Request:  Host=api.github.com,  verändernd=ja (POST)
   in der Decke {Hosts:[api.github.com], Schreibrecht:ja}?  → JA → weiter
   (Host=evil.com wäre hier hart geblockt, ohne Prompt)
   │
   ▼
🚪 TOR 2  FREIGABE  (Mensch)
   verändernd → Policy: fragen.  Stehender Grant für github.issue_anlegen?
     ja  → still durch
     nein→ HITL out-of-band.  Wahl merkt:  github.issue_anlegen @ api.github.com
   kritisch? → nie „immer", fragt jedes Mal
   │
   ▼
echter Effekt
```

---

## 3. Die A/B-Zerlegung — Reichweite vs. Wirkung

„Einen Host *lesend/schreibend* erreichen" ist schief — es klebt zwei Achsen zusammen.
Richtig sind **zwei getrennte Achsen**:

- **A) Reichweite** — *welche Hosts* sind erreichbar. Binär (an/aus), protokoll-egal
  (HTTP und MCP zum selben Host teilen sie). Kein lesend/schreibend hier.
- **B) Wirkung** — *verändert die Operation die Welt?* Eigene Achse. Lesen ≠ Schreiben.

### Die Decke (install-reviewt) hat zwei Teile
- **Erlaubte Hosts:** `["api.github.com"]` — reine Reichweite (A).
- **Schreibrecht:** darf dieses Plugin überhaupt verändern? (B, grob). Ein „nur-lesend"-
  Plugin kann *strukturell* nichts mutieren, egal was seine Tools versuchen.

### Jedes Tool deklariert drei Dinge
- **Klartext** — der Satz, den der Mensch liest: „Issue in {repo} anlegen".
- **Art** — `lesend` oder `verändernd`.
- **kritisch** — randgefährlich/unumkehrbar (löschen, zahlen)? ja/nein.

### Die Policy (die stehende Regel)
- `lesend` → läuft still (Lesen fragt nie — wie jedes Referenzsystem).
- `verändernd` → fragt (außer stehender Grant für *dieses Tool*).
- `kritisch` → fragt **immer**, kann nie „für immer" werden (never-auto-Floor = der Moat).

### Sicherheits-Feinheit: „Art" wird erzwungen, nicht geglaubt
`lesend/verändernd` wird, **wo der Host die echte Operation sieht** (HTTP-Methode:
GET=lesend, POST/PUT/PATCH/DELETE=verändernd, `netcap/netcap.go:148`), **aus ihr
abgeleitet** — nicht aus der Tool-Deklaration. Sagt ein Tool „lesend", macht aber POST →
gilt „verändernd" (das Strengere gewinnt). Nur bei **MCP**, wo wir die echte Operation
hinter dem Server nicht sehen, verlassen wir uns auf die Deklaration (`readOnlyHint`) —
begrenzt durch den Zaun auf den einen erlaubten Host.

---

## 4. Host-Matching — mehrere Domains, Subdomains

**Regel: ein Host-Eintrag matcht exakt. Subdomains muss man bewusst schreiben.**
(Hausregel „explizit statt implizit, fail-closed"; `path.Match` + `Wildcard`.)

- `hosts: ["github.com"]` deckt **nur** `github.com`. Request an
  `x.y.github.com` → **hart am Zaun geblockt, es wird nicht gefragt**.
- Subdomains explizit: `hosts: ["*.github.com"]` (deckt jede Subdomain, nicht den Apex).
- Beides: `["github.com", "*.github.com"]`.
- Mehrere Domains = Liste; jeder Eintrag exakt oder explizit-Wildcard:
  ```jsonc
  hosts: ["api.github.com", "uploads.github.com", "*.githubusercontent.com"]
  ```
  Kein implizites „Apex deckt Subdomains" — Subdomains sind oft andere Trust-Grenzen
  (`pages.github.com`/`*.github.io` = User-Content). Browser (Origin), CSP, Firewalls
  machen es alle explizit. `raw.githubusercontent.com` ist zudem eine andere
  Registrable-Domain — es unter `github.com` zu vermuten wäre der Cookie-Footgun.

**Consent sieht nur Hosts *innerhalb* der Decke** (der Zaun filterte vorher). Die
**Merk-Granularität = der Decken-Eintrag, der den Call durchließ**: exakter Host → Grant
`@ api.github.com`; Wildcard → Grant `@ *.github.com` (das reviewte Muster). So kein Ask
pro Subdomain, wenn der Autor legitim einen Wildcard deklariert — und nie breiter merkbar
als beim Install freigegeben. Zusammen mit der Tool-Achse bleibt selbst ein Wildcard-Grant
eng: „immer **issue_anlegen** @ *.github.com" ≠ „immer alles".

Der Prompt zeigt beides — exakter Host (Transparenz) + gemerktes Muster (Ehrlichkeit):
```
Issue in acme/web anlegen
via issue_anlegen → schreibt auf  x.y.github.com          ← was JETZT passiert
  [ einmal ]  [ Session: issue_anlegen ]
  [ immer: issue_anlegen @ *.github.com ]                 ← was gemerkt wird
  [ ablehnen ]
```

---

## 4b. Cage (= Tor 1 / Zaun): geschachtelt & der Konflikt-Fall

Die **Cage** (`capability.Cage`) ist die harte Reichweiten-Grenze —
Tor 1 aus §2. Sie ist ein **komponierbarer oberer Bound** auf das, was überhaupt
*versucht* werden darf (Family + Target + read/write). **Außerhalb = hard deny, nie ein
Ask** (der Anti-Injection-Boden: man kann nicht dazu gebracht werden, etwas freizugeben,
das gar nie erreichbar war).

**Cages schachteln sich per Schnittmenge** (ctx-Kette, `WithinCages` = UND über alle):
```
Workspace-Cage  ⊃  Plugin-Cage (manifest requires)  ⊃  Agent-Cage (cage:)  ⊃  MCP-Cage
```
Eine innere Cage kann nur **subtrahieren**, nie erweitern — ein gekaperter innerer Scope
(injiziertes Plugin/Agent) kann **strukturell nicht** aus der äußeren Cage ausbrechen
(monotone Attenuation). **Default: keine Workspace-Cage** (leere Kette = fail-open, nur die
Policy gilt) → Cages sind **Opt-in** für den Walled-Garden-Fall.

### Der Konflikt-Fall (Workspace ∩ Plugin = ∅)

Wenn der Workspace auf `google.de` gecaged ist, ein Plugin aber `github.com` braucht, ist
die Schnittmenge **leer** → das Plugin erreicht **nichts**, jeder Call ist hard-denied. **Die
Cage gewinnt immer** (sonst wäre sie wertlos — eine Cage, die jede Installation aufweicht,
ist keine). Das ist **nicht verhandelbar.**

Das Problem ist damit reine **UX, nicht Semantik**: ein stumm totes Plugin ist der Footgun.
Regel: **Konflikte werden am Trust-Boundary (Install/Load) sichtbar gemacht — NIE stumm zur
Laufzeit.** Der Install-Review cross-checkt die Plugin-/Agent-Reichweite gegen die
Workspace-Cage.

### Der Remedy: Hallway (NICHT die Workspace-Cage per JSON weiten)

Die Workspace-Cage zu weiten (JSON `{google.de, github.com}`) ist **unmanageable + orphan-
anfällig**: entfernt man das Plugin, bleibt `github.com` vergessen in der Workspace-Cage stehen
und erlaubt es plötzlich auch dem **Modell direkt** — stille Rechte-Regression. Stattdessen:

> **Ein explizit erlaubtes Plugin etabliert einen HALLWAY** — einen Korridor durch die
> Workspace-Mauer, **exakt so breit wie seine eigene Cage** (nicht breiter). Für die ctx
> *dieses* Plugins wird die Cage-Kette **auf seine Cage zurückgesetzt** (statt Schnittmenge):
> `[Workspace, Plugin]` → `[Plugin]`. Nur `github.com` gilt, es „sprengt die Kette".

```
Plugin "github" will github.com — außerhalb der Workspace-Cage (google.de).
  [ Hallway erlauben: erreicht github.com, sonst nichts ]     ← der saubere Weg
  [ Eingesperrt lassen: Workspace ∩ Plugin (= funktionslos) ]
  [ Abbrechen ]
```

**Warum sicher — der Hallway ist NICHT „Cage aus", sondern „seine Cage statt Workspace∩seine":**
1. **Genau die deklarierte Plugin-Cage breit** — nie weiter (das github-Plugin kann durch den
   Hallway nie nach `evil.com`).
2. **Explizit + reviewt** beim Install; das *ist* die bewusste Weite-Entscheidung — aber an den
   **Plugin-Lifecycle gebunden**: Uninstall → Hallway weg, **atomar, kein Orphan** (löst
   FRAGEN #8.1 purge-on-removal für Cages; selber Geist wie per-Owner-Grants, §9).
3. **Scoped auf die Plugin-ctx** — Modell, Session, native Tools, andere Plugins bleiben in der
   Workspace-Cage.
4. **Policy-Deny bleibt der globale Boden:** der Hallway sprengt nur die *Cage*-Kette, nicht die
   Base-Policy. Ein globales „deny X" gilt **durch den Hallway hindurch** (deny-wins, immer).

Damit schärft sich die Bedeutung der Workspace-Cage: sie bounded die **ambiente/untrusted**
Autorität (Modell + Injection + Session); **explizit installierte Plugins tragen ihre eigene
reviewte Autorität und hallwayen hindurch.** Ein Workspace-Flag **„keine Hallways"** macht die
Mauer für Compliance absolut (kein Plugin kann raus).

**Impl-Skizze:** heute `runGuest` = `WithCage(ctx, p.cage)` (append). Hallway = zweiter
Modus `WithOnlyCage` (Kette **reset**), Flag `hallway:true` im approved-Record. Rest (Policy,
HITL, Grants) unverändert. Gehört mit Workspace-Cage + Nutzer-Attenuation in **einen** späteren
„Cage-Layer"-Schritt.

### Cage vs. Policy — zwei Werkzeuge für „nur google.de"

| | **Workspace-Cage** (hart) | **Workspace-Policy `ask`** (weich) |
|---|---|---|
| Semantik | google.de = Gitterstäbe; Rest existiert nicht | „außerhalb google.de → fragen" |
| Plugin auf github | **tot**, kein Ask | **funktioniert**, aber jeder Effekt fragt |
| Gefühl | Walled Garden, unpassierbar | Kontrollpunkt, passierbar mit Zustimmung |
| Injection | github strukturell unmöglich | möglich, wenn Mensch zustimmt |

**Merksatz:** Cage = *was ist überhaupt denkbar* (hart, strukturell); Policy = *wo will ich
gefragt werden* (innerhalb des Denkbaren). Für „Workspace-weit nur google.de" ist die
**Policy-`ask`** meist benutzerfreundlicher (Plugins bleiben nutzbar, Ausbrüche prompten);
die **Cage** ist für echten Walled-Garden (Compliance/hochsensibel) — dann ist die
Konflikt-UX beim Install **Pflicht**, sonst Footgun.

---

## 4c. Zwei Cage-Ebenen: Action (Welt) vs. Reach (Umgebung)

Eine Cage ist nicht *eine* Sache — sie lebt auf **zwei Ebenen**, weil zwei verschiedene
Dinge eingesperrt werden. Eine reine Tool-Einschränkung reicht **nicht**, weil es
**generische Reach-Tools** gibt (`http`, `file`, `ssh`), die ein rohes Rohr sind: das Tool
zu *haben* sagt nichts darüber, *wohin* es feuert.

| Ebene | Frage | Wer deklariert | Format |
|---|---|---|---|
| **Action-Cage** (die *Welt*) | welche **Tools**? | Agent `tools:`, Session = alle | Tool-Namen (`gmail.*`, `ssh`) |
| **Reach-Cage** (die *Umgebung*) | welche **host-calls**? | Workspace (Umgebung), Plugin, Agent *optional* | `{family, target, access:[…]}` |

- **Gezielte Tools** (`gmail.send`) tragen ihr „wohin" schon in sich (das Plugin bringt seine
  Reach-Cage mit) → Tool-Liste genügt fast.
- **Generische Tools** (`ssh`, `http.write`, `file.write`) NICHT: `tools:[ssh]` heißt „darf
  sshen", **nicht** „nur nach 192.168.*". → hier braucht man **zwingend beide**: die Tool-
  Liste („darf sshen") **und** eine Reach-Cage („wohin").

### Die Workspace-Reach-Cage = die Umgebung

„Der Workspace darf nur ins lokale Netz" ist eine **Workspace-Reach-Cage** — die äußerste
Umgebung, die **alles darin** (Session + jeden Agent + jedes Plugin) per Schnittmenge bounded:
```yaml
# workspace-config (die Umgebung)
cage:
  - { family: http, target: "192.168.*", access: [read, write] }
  - { family: ssh,  target: "192.168.*", access: [read, write] }
tools: [http.read, http.write, ssh, file.read, file.write]   # was überhaupt existiert
```
Ein `http.fetch google.com` ist damit für **alle** strukturell tot (hard-deny, kein Ask) —
das ist, was die sonst host-unbegrenzte Session einfängt (heute hat sie keine Reach-Cage →
leere Kette → nur Policy fragt, host-unbegrenzt; die Umgebung fehlt noch, s. Cage-Layer-TODO).

Ein Agent gliedert sich per Schnittmenge ein:
```
Agent-effektiv = tools: (Action) ∩ Workspace-Reach-Cage ∩ [Agent-eigene Reach-Cage optional]
```
Ein `ssh`-Agent mit eigener Cage `{ssh, 192.168.1.5}` kann genau diesen einen Host — selbst ein
injiziertes `ssh 8.8.8.8` ist doppelt tot (Workspace-Cage **und** Agent-Cage).

**Merksatz:** *Tools = was getan werden darf (Action). Umgebung = womit die Welt berührt
werden darf (Reach). Für generische Tools (ssh/http) brauchst du beide — sonst ist „darf
sshen" = „darf gegen die ganze Welt sshen".*

### Cage ist ALLOW-only — Ausschlüsse (NOT) leben in der Policy

Frage: kann man in der Cage `http NOT 192.168.2.0/24` sagen? **Nein — bewusst nicht.** Die
**Cage ist rein positiv (allow-only): sie zählt auf, was erreichbar IST.** Ausschlüsse/Denys
gehören in die **Policy** (die hat `deny>ask>allow`). „http überall außer 192.168.2.*" =
breite Cage + Policy-`deny` auf `192.168.2.*`.

**Warum kein `allow`+`deny` in der Cage** (der verworfene Zwei-Schlüssel-Vorschlag): dann
würden **Plugin-Autoren faul** — sie schrieben „allow `*`, deny `evil`" statt eng zu listen,
was sie brauchen. **Allow-only erzwingt Least-Privilege** (positive Enumeration). Deny gibt es
nur auf der **Policy**-Ebene (Operator/Agent-Autor, trusted) — nicht in der Autoren-Cage.

Damit die Trennung:
- **Cage** = allow-only, positiv (was ist überhaupt erreichbar). Erzwingt Least-Privilege.
- **Policy** = allow/ask/**deny** (Ausschlüsse, Blacklists, „außer X"). Deny-wins.

**Matcher-Notiz:** `target` wird per **Glob** gematcht (`path.Match`), nicht CIDR — also
`192.168.2.*` (≈ /24) oder `192.168.*` (≈ /16), aber **nicht** `192.168.2.0/24` (der `/`
matcht nicht). Echte CIDR-Prefixe (/23, /25 …) bräuchten einen eigenen IP-Matcher → späterer
Ausbau.

---

## 5. Terminologie (Vorschlag — überschreibbar)

„Capability" und „Host-Primitiv" verschwinden aus der Nutzer-Sprache; sie werden interne
Klempnerei. Der Mensch sieht nur: Hosts, und pro Tool (Klartext / lesend-verändernd /
kritisch).

| Konzept | Term (Vorschlag) | Alternativen |
|---|---|---|
| welche Hosts erreichbar (A) | **Erlaubte Hosts** (bei Dateien: *Erlaubte Ordner*) | Reichweite · Zugänge |
| verändert es die Welt? (B) | Tool ist **lesend** / **verändernd** | schreibend · mutierend |
| grobe Schranke „darf ändern" | **Schreibrecht** | Änderungsrecht |
| randgefährlich/unumkehrbar | **kritisch** | heikel · folgenschwer |
| Klartext-Satz pro Tool | **Klartext** | Beschreibung · Intent |
| Tor 1 (Host prüfen, still) | **Zaun** | Reichweiten-Check · Confine |
| Tor 2 (Mensch fragt) | **Freigabe** | Zustimmung · Consent |
| interne http/file/dns-Ebene | *(kein Nutzer-Name)* | Systemzugriff · Grundzugriff |

Im Code bleibt Englisch (`hosts`, `access:[read,write]`, `tool.mutates`,
`tool.consequential`, `tool.intent`).

---

## 6. Die drei Jobs — Einheiten aufgelöst

Der Kern-Befund der ganzen Analyse: drei verschiedene Jobs mit heute **drei
inkonsistenten Granularitäten**. Endzustand:

| Job | Frage | Einheit (Ziel) | Warum |
|---|---|---|---|
| **Gate** (Tor 1+2) | Darf es? | Zaun: `Host + verändert?` (host-berechnet) · Freigabe: `Tool × Host` | Zaun unfälschbar; Freigabe verständlich |
| **Show** (HITL) | Was liest der Mensch? | **zweizeilig**: Klartext-Kopf + host-berechnete Faktenzeile — **immer beide** | Semantik allein täuschbar; Transport allein unlesbar |
| **Remember** (Grant) | Was wird gemerkt? | **`Tool × Host`** + Scope | deckt exakt das Gezeigte; schließt das Loch (§7) |

Der Tool-Schlüssel ist der äußerste modell-sichtbare Tool-Name der Kette
(`github.issue_anlegen`, `http`, `code.run`) — host-vergeben (`tool.Registry`), nicht
fälschbar. Für Skripte heißt das bewusst: `code.run × host` ist ein *anderer* Grant als
ein direkter `http × host` — Autorität wird dem Pfad zugerechnet (wie Claude Codes
`Bash(curl …)` ≠ `WebFetch(domain:…)`).

---

## 7. Das Loch heute — warum das MUST zuerst kommt

Verifiziert am Gmail-Plugin (`plugins/gmail`, Tools `search`+`send`, beide über
`http.read/write @ gmail.googleapis.com`):

1. Modell ruft `gmail.send{to:"bob@x"}`.
2. HITL zeigt (dank Intent-Template): **„Send an email to bob@x"**. Mensch wählt **„Allow always"**.
3. Gemerkt wird heute: `(default, http.write, gmail.googleapis.com)` (`agent/grants_store.go:68`).
4. **Ab jetzt ist JEDER Write jedes Aufrufers an gmail.googleapis.com still erlaubt** —
   `gmail.send` an beliebige Empfänger, ein späteres `gmail.delete`, ein Skript via
   `code.run`, das Modell direkt. Der Mensch las „E-Mail an Bob" und gewährte „alle
   Gmail-Writes für immer".

Identisch bei MCP: alle Tools eines Servers kollabieren auf `http.write @ <mcp-host>`
(`mcpcap/mcpcap.go:141`) — „always" auf „github: create_issue" erlaubt still auch
`delete_repo`. **Genau der Prompt-Injection-Hebel, gegen den das System gebaut ist:** die
Injection braucht kein neues Ziel, sie reitet auf dem breiten Grant unter schmalem Wording.
Der tool-gekeyte Grant (§6) schließt das — unabhängig vom Rest.

---

## 8. HITL-Wording — ein kohärentes Modell

Format für jeden Ask:
```
Approve   Send an email to bob@example.com                        ← Klartext (trusted)
          via gmail.send → schreibt auf gmail.googleapis.com      ← Faktenzeile (host-berechnet, Pflicht)
          plugin: gmail · workspace: default
  [ einmal ]  [ Session: gmail.send ]  [ immer: gmail.send @ gmail.googleapis.com ]  [ ablehnen ]
```
Regeln:
1. **Klartext-Kopf** = trusted Intent, Priorität Plugin-Template > host-Semantik (MCP) >
   Transport-Default. Die heutige `WithIntent`-Kette (`gateway.go:84`), unverändert gut.
2. **Faktenzeile ist Pflicht und unlöschbar** — nur aus dem host-konstruierten Call +
   Tool-Namen, nie aus Gast-/Modell-Text. Der Intent *ergänzt* sie, ersetzt sie nicht
   (heute ersetzt er sie — ändern). Anti-Täuschungs-Boden.
3. **Ehrlichkeitsregel:** jedes Scope-Label benennt die Merk-Einheit wörtlich („immer:
   gmail.send @ gmail.googleapis.com"), nie ein blankes „Allow always". Args (`to`,
   Betrag) gehören in den Klartext-Kopf (Einmal-Entscheidung), **nie** in den
   Grant-Schlüssel.
4. `WithIntent` nur aus trusted Layern (Plugin-Host, MCP-Adapter) — als Invariante mit
   `SECURITY:`-Kommentar festschreiben; nie aus Gast-Code.

---

## 9. Attended vs. unattended + Autonomie & Grant-Scoping

**Scope-Leiter (Merk-Ebenen) — strikte Owner-Isolation, KEIN Cross-Tier-Inheritance:**

| Scope | Lebensdauer | Persistenz (per-Owner-Datei) |
|---|---|---|
| `einmal` | 1 Call | — |
| `Session` / `Run` | bis Epoch-Close | in-memory, epoch-gebunden |
| `dieses-Owners-immer` | dauerhaft, **nur dieser Owner** | Session/Workspace → `<ws>/grants.json` · Agent → `<ws>/agents/<name>/grants.json` |

**Jeder Owner ist eine Insel. Kein Lookup über Tiers hinweg** (früher hier fälschlich
„eigenes ∪ ws" — verworfen). Grund: die interaktive **Session ist trusted** (Mensch sitzt
davor), ein **Agent verarbeitet untrusted Input** (Mail/Web = die Injection-Fläche). Würde
ein Agent die Session-Grants erben, empowerte ein bequemer Session-Grant („immer
gh.issue_anlegen") still einen injizierten Mail-Agenten → Broker umgangen. Also: ein Grant
gilt **nur** für den Owner, der ihn erteilt hat. Der Convenience-Verlust ist gering (Reads
sind eh still, §3; Writes pro Owner bewusst neu zu entscheiden ist *korrekt* — anderer
Trust-Kontext). Die **per-Owner-Datei macht die Isolation strukturell** (getrennte Dateien
können nicht quer-matchen), löst Purge-on-removal gratis (Agent-Ordner löschen = seine
Grants weg) und macht einen Agenten zur portablen self-contained Einheit (ADR-10).

**Agent-als-Ordner:** `agents/<name>.md` → `agents/<name>/{agent.md, grants.json}` (weiter
außerhalb `mnt/`, Control-Plane — das Modell darf Grants nie schreiben). Jeder Owner bekommt
seinen **eigenen** file-backed `GrantsStore` (ein Mutex, ein Owner, Records ohne
`grant_set`-Feld — die Datei *ist* der Owner); `capability.Grants.ID` wird damit fast
überflüssig. **Ein „Workspace-weit"-Tier** (empowert *alle* Aufrufer) bleibt **deferred** —
falls je gebaut, nur als **expliziter, klar gelabelter Opt-in**, nie impliziter Lookup, und
für unattended Agents gesperrt.

### Grant vs. Policy — die load-bearing Trennung (korrigiert)

Beide sind unter der Haube `capability.Rule`, aber sie sitzen an **verschiedenen
Pipeline-Stufen** mit **verschiedener Semantik** — und das entscheidet, was wohin gehört:

| | **Policy** | **Grants** |
|---|---|---|
| Herkunft | **Autor-Config** (statisch, trusted) | **Menschen-Antworten zur Laufzeit** (dynamisch) |
| Rolle | erzeugt das **Urteil** (deny>ask>allow) | wird **nur bei Ask** konsultiert → kürzt ihn ab |
| Ask überschreiben? | nein (Policy-`Allow` verliert gegen Policy-`Ask`) | **ja** (ihr einziger Job) |
| Deny? | ja (deny-wins) | nein (Grants sind nur „Ask beantwortet") |

**Konsequenz (Kern-Klärung):** Ein Grant ist **kein** „Allow-Rule", sondern eine
**vorbeantwortete Ask-Frage.** Deshalb gehören **Grants NIE in eine Agent-*Definition*** —
eine `.md` ist Autor-Config, kein Laufzeit-Consent; Grants dort reinzuschreiben hieße,
Menschen-Zustimmung zu fälschen (der Grund, warum sie außerhalb `mnt` liegen). **Was der
Agent-Autor an stehender Autorität will, deklariert er als Policy** — nicht als „vorgesäte
Grants" (das war ein Fehler früherer Fassungen).

### Ein Scope = zwei deklarative Knöpfe (Workspace UND Agent)

Kein neues Konzept: ein Agent ist ein **Scope** mit denselben zwei Knöpfen wie der Workspace.

| Knopf | Was | Semantik |
|---|---|---|
| **Cage** | Reichweite/Schreibrecht, hart | Schnittmenge (ctx-Kette), außerhalb = hard-deny, nie fragen |
| **Policy** | Urteil (allow/ask/deny) | deny>ask>allow, komponiert mit der Base |

- **Workspace** setzt die Base-Policy (`lesend→Allow, verändernd→Ask`).
- **Agent** legt seine **eigene Policy** (+ optional Cage) obendrauf.
- **Grants** liegen *unter* beidem, rein zur Laufzeit.

### Tightening jetzt, Loosening (Autonomie) = Phase 4

In `deny>ask>allow` gilt **ask schlägt allow**. Daraus:
- **Tightening funktioniert mit flacher Union sofort:** Agent-**Deny** gewinnt gegen alles
  (Blacklist); Agent-**Ask** wo Base allowt (z. B. Reads erzwingen fragen) — ask schlägt allow.
- **Loosening ist der Autonomie-Fall:** Agent-**Allow** wo Base **Ask** sagt (`full`, „writes
  still") geht **nicht** per Union (ask>allow). Braucht eine **Präzedenz**:
  > **Cage > Consequential-Floor > Deny (jede Ebene) > Agent-Urteil > Base-Urteil > Grants.**

  So kann der Agent loosen **und** tighten, aber Deny/Cage/Floor bleiben unantastbar
  darüber. Das ist der **Autonomie-Regler** — nicht als Preset-Grants, sondern als
  **Agent-Policy** (der Autor schreibt/wählt sie). **Erst mit cron** (unattended pending).

| Level (Phase 4) | = Agent-Policy | für |
|---|---|---|
| `strict` | `verändernd→Ask` (auch Reads: `lesend→Ask`) | Debug, hochsensibel |
| `guarded` (**Default**) | nichts extra (= Base: Reads still, Writes fragen) | untrusted-Input |
| `full` | `verändernd→Allow` für die Agent-Hosts (Präzedenz > Base-Ask), **minus kritisch-Floor** | trusted Pipelines |

Read/verändernd-Klassifikation (für Preset-Ableitung): nativ per HTTP-Methode, Plugin per
Tool-`mutates`, MCP per `readOnlyHint`; unklassifizierbar = verändernd (fail-closed).

**Der Unterschied attended/unattended:**

| | Attended (Session, manueller Run) | Unattended (cron — noch nicht gebaut) |
|---|---|---|
| lesend | still | still (guarded-Preset — sonst pingt das Handy für Wetter) |
| verändernd | Ask inline **oder** Handy (Mensch da) | Ask **out-of-band pending**, `deadline`-Budget pausiert (`hitl/engine.go:120`); Timeout/Deny ⇒ *dieser* Effekt failt sauber ans Modell, Run läuft weiter |
| kritisch | fragt immer | fragt immer (pending; keine Antwort ⇒ passiert nicht) |
| Damage-Bounds | Rate/Window optional | Rate + `Window` (nur 9–18 h) + Budget — alle Primitive existieren |

Kern-Einsicht (FRAGEN #10, bestätigt): ein attended `guarded`-Run **ist** eine Session —
der Regler wird erst mit cron scharf. Aber Merk-Einheit (§6) und Policy-Klassen (§3)
lohnen **sofort**, attended.

---

## 10. Benchmark (verdichtet)

Alle führenden Systeme teilen vier Muster, die dieses Zielbild übernimmt:
1. **Grant-Einheit = das Gesehene** (Claude Code `Tool(specifier)`, GPT-Actions per Domain,
   iOS App×Capability) — nie schmal zeigen, breit merken.
2. **Lesen still**, gefragt bei Schreiben/Erst-Kontakt.
3. **Ein Autonomie-Regler**, Default sicher — niemand erzwingt Per-Effekt-Fragen im Auto-Modus.
4. **Eine never-auto-Klasse** bei den Sorgfältigen (GPT `x-openai-isConsequential`,
   iOS-Zahlung) — manches kann man nie auf „immer" schalten. = unser `kritisch`-Floor.

Nocturns Sonderfall bleibt: der Entscheider ist ein LLM mit untrusted Input → Injection
kann *innerhalb* verbundener Accounts kapern. Antwort ist nicht „jeder Write fragt jedes
Mal", sondern schmale ehrliche Grants + kritisch-Floor + Damage-Bounds + hartes Ask bei
jeder Autoritäts-Erweiterung. Genau das leisten die vorhandenen Primitive neu arrangiert.
*(Produkt-Details bei Cline/Goose/Claude-Code-Subdomain-Semantik vor externem Zitat prüfen.)*

---

# TEIL B — IMPLEMENTIERUNGSPLAN (zum Absegnen)

**Prinzip:** jede Phase ist **allein shipbar**, `go build ./... && go test -race ./...`
grün als Abschluss-Tor, Broker-Kern erst ab Phase 1. Greenfield-Stil: alte
`grants.json`-Formate werden **verworfen** (kein Compat-Cruft — der Nutzer beantwortet die
Frage einmal neu), keine Wrapper stehen lassen.

Empfohlene Reihenfolge: **Phase 0 zuerst** (schließt das Loch §7, minimal-invasiv,
unabhängig von der Achsen-Frage), dann der Achsen-Refactor. Phase 0 fasst `grants.go`
zweimal an (jetzt + in Phase 1) — bewusst akzeptiert: das Loch schließt in Tagen, nicht
nach dem großen Refactor. **Alternative** (falls du lieber einmal am Kern operierst): 0+1
mergen — größere erste PR, langsamer zum Fix. → *deine Wahl beim Absegnen.*

---

### Phase 0 — MUST: Freigabe tool-genau + ehrlicher Prompt  *(schließt §7)* — ✅ ERLEDIGT
**Ziel:** „immer" merkt Tool×Host statt nur Capability×Host; Prompt zweizeilig; Labels ehrlich.
**Umgesetzt (build/vet/`test -race` grün):**
- `internal/tool/registry.go` — `withToolName`/`ToolName(ctx)`, outermost-wins-Stempel in `Invoke`
  (Plugin `gmail.send` bleibt der Grant-Name, nicht das innere `http`).
- `internal/gateway/gateway.go` — importiert `tool`; liest `tool.ToolName(ctx)`; `approvalChoicesFor`
  (ehrliche Labels „Allow always: gmail.send @ host") + `factLine` (Intent *ergänzt*, ersetzt nicht).
- `internal/capability/grants.go` — `Allows(tool,…)`/`Record(tool,…)`, `GrantStore`-Interface + `sessionGrant`.
- `internal/agent/grants_store.go` — `grantRecord.Tool`; alte tool-lose Records fallen fail-closed raus.
- `cmd/nocturn/tui.go` — zweizeiliger Prompt (Kopf + faint Faktenzeile); `cmd/nocturn/mcp.go` — Setup-Grant tool="".
- Regression: `internal/capability/grants_test.go` („always gmail.send" ⇏ gmail.delete; session tool-scoped + epoch-bound).
**Historische Änderungsliste (Referenz):**
- `internal/tool/registry.go` — äußersten Tool-Namen in ctx stempeln (outermost-wins,
  Muster wie `withCallID`, Z. 56/137), abrufbar für den Guard.
- `internal/gateway/gateway.go` — Tool-Namen aus ctx lesen; an `Grants.Record/Allows`
  durchreichen; Choice-Labels pro Ask aus `(tool, cap, target, scope)` rendern
  (statt statischem `approvalChoices`, Z. 47); Faktenzeile bauen (Intent ergänzt, ersetzt nicht).
- `internal/capability/grants.go` — `Record`/`Allows` um `Tool` erweitern.
- `internal/agent/grants_store.go` — `grantRecord` + Feld `Tool`; alte Records verwerfen.
- `cmd/nocturn/tui.go` — zweizeiliger Prompt (Klartext-Kopf + Faktenzeile), ehrliche Labels.
**Fertig wenn:** Regressionstest „always gmail.send erlaubt nicht gmail.delete"; Prompt
zeigt beide Zeilen. **Risiko:** niedrig (kein Broker-Kern). **Shipbar:** ja, sofort.

### Phase 1 — Reichweite/Wirkung-Split (der Achsen-Refactor, Zielbild-Kern) — ✅ ERLEDIGT
**Ziel:** „Capability" (protokoll.verb) → zwei Achsen; der Zaun prüft Host+verändert?,
die Policy eine Regel `verändernd→Ask, lesend→Allow`.
**Umgesetzt (build/vet/`test -race` grün):**
- `internal/capability/broker.go` — `Call{Family, Mutates, Target}`; **`Match`** (MatchNone/Read/
  Write/Any, fail-closed-Null) auf `Rule`/`Pair` = die Schreibrecht-Achse; `matches` prüft `Writes.covers(Mutates)`.
- `internal/capability/cage.go` — `Pair{Family, TargetGlob, Writes}`.
- `internal/capability/grants.go` + `internal/agent/grants_store.go` — Schlüssel `(Tool, Family, Mutates, Target)`.
- `internal/netcap/*` — `mutatesForMethod` (GET=false, POST/…=true), Family=http; Credential family-scoped.
- `internal/filecap/*` — Family=file, Mutates aus read/write. `internal/mcpcap/*` — ein `http`-Reach `MatchAny`.
- `internal/plugin/{manifest,install}.go` — **kein Legacy-Bridge**: `Require{Family, Target, Mutates}`
  (`mutates:true` = read+write, write⊇read), `CredentialDecl{Family}`; `plugin.json`-Manifeste migriert.
- `internal/secret/*` — Callers injizieren mit `Family` ("http"); `Binding.Capability` bleibt generischer Scope-String.
- `cmd/nocturn/app.go` — Base-Policy: `lesend→Allow, verändernd→Ask` (eine Regel-Paar).
**Erreicht:** http/dns/file identisch gegated wie vorher (Bestandstests angepasst grün); `lesend` läuft still.
Grant-Host-Granularität (matchende Cage-Entry, §4) + Ordner-Normalisierung → **Phase 3**.

### Phase 2 — kritisch-Floor + Rate-Härtung + MCP-Reads-still — ✅ ERLEDIGT
**Ziel:** never-auto-Floor; Grant-Pfad respektiert Rate; read-only MCP-Tools laufen still.
**Umgesetzt (build/vet/`test -race` grün):**
- `internal/plugin/manifest.go` — `ToolDecl.Consequential bool` (install-reviewt, trusted).
- `internal/gateway/gateway.go` — `WithConsequential`/`consequentialFrom`; ein consequential Effekt
  **fragt immer** (eskaliert sogar Allow→Ask), Grants werden **nicht** konsultiert, Choices =
  `consequentialChoices` (nur once/deny), kein Record. + **Rate-Check auf dem Grant-Kurzschluss-Pfad**
  (Regression: „immer"-Grant kann nicht unbegrenzt repliziert werden).
- `internal/plugin/runner.go` — stempelt `WithConsequential`, wenn `ToolDecl.Consequential`.
- `internal/mcpcap/{tools,mcpcap}.go` — `readOnlyHint` → per-Tool ctx-Flag → `Call.Mutates=!readOnly`
  (read-only MCP-Tool = read = still; Setup/Writes = Ask). Decke bleibt read+write (MatchAny).
- `cmd/nocturn/plugins.go` — Install-Review markiert consequential Tools.
- Tests: `TestAuthorize_Consequential_NeverAutoNeverRemembered` (fragt trotz Grant, nur once/deny),
  `TestAuthorize_StandingGrantRespectsRateCap`.
**Hinweis:** Der Manifest-Achsen-Umbau (`requires{family,target,mutates}`, `CredentialDecl{family}`,
Base-Policy reads-Allow/writes-Ask) wurde bereits in **Phase 1** mitgezogen (kein Legacy-Bridge).
Offen für **Phase 4**: per-Tool `does:read/write` für die Autonomie-Preset-Ableitung.

### Phase 3 — Per-Owner-Grant-Dateien (Agent-als-Ordner) + Grant-Sichtbarkeit
**Ziel:** strukturelle Owner-Isolation (§9, KEINE Union), Purge-on-removal gratis, Grants
einsehbar/widerrufbar.
**Änderungen:**
- Layout: `agents/<name>.md` → `agents/<name>/{agent.md, grants.json}` (außerhalb `mnt/`);
  `internal/agent/definition.go` (`LoadAgents` liest `<dir>/<name>/agent.md`), `cmd/nocturn/app.go`.
- `internal/agent/grants_store.go` — Records ohne `grant_set`-Feld (Datei = Owner); pro Owner
  eine Store-Instanz an eigenem Pfad.
- `internal/agent/session.go`, `run.go` — Session-Store `<ws>/grants.json`, RunTask-Store
  `<ws>/agents/<name>/grants.json`; **kein Cross-Tier-Lookup** (jeder Owner nur seine Datei).
- `internal/capability/grants.go` — `ID` wird optional/entfällt (ein Store = ein Owner).
- `cmd/nocturn/*` — `/grants`-Listing + Widerruf; Host-Match-Granularität aus der matchenden
  Cage-Entry (exakt vs. Wildcard) im Label.
- **Deferred:** ein explizites „Workspace-weit"-Opt-in-Tier (nie impliziter Lookup, für
  unattended gesperrt) — erst falls real nötig.
**Fertig wenn:** `/grants` zeigt+widerruft; Agent-Ordner-Löschen entfernt seine Grants; ein
Session-Grant empowert **keinen** Agenten (Isolationstest); Wildcard-Grant deckt Subdomains, exakter nicht.
**Risiko:** niedrig. **Shipbar:** ja.

### Phase 4 — (nur mit cron) Autonomie-Regler
**Ziel:** unattended Agents; `full/guarded/strict`.
**Änderungen:** `internal/agent/definition.go` (`autonomy:`), `run.go` (Preset-Seeding als
vor-gesäte epoch-Grants), Read/verändernd-Klassifikation (Phase 2 liefert die Metadaten),
unattended Pending-UX (ntfy + `deadline` stehen). **Status:** zurückgestellt bis cron existiert.

---

## Nicht angefasst (bleibt stabil)
Broker-`decide`-Präzedenz, Epoch, HITL-Engine/Token, Sandbox, `deadline`. Das Zielbild ist
Re-Arrangement + zwei kleine Erweiterungen (Tool-Schlüssel, ws-Tier) + ein Achsen-Refactor
der bestehenden Primitive — kein Kern-Neubau.

## Offene Punkte (bewusst später)
- Parameter-Constraints (`gmail.send` nur an `@firma.de`) — erst bei realem Bedarf.
- Glob-Widening im HITL über den Normalisierer hinaus — Default exakt/normalisiert.
- Ask-Batching mehrerer pending Writes (unattended) — reine `hitl.Notifier`-UX.
- Purge-on-removal (FRAGEN #8.1) — Reconcile über grants/secrets/approved.

---

## Freigabe-Checkliste (bitte abzeichnen)
- [ ] Zielbild §2–§4 (zwei Tore, A/B-Zerlegung, Host-Matching) ok?
- [ ] Terminologie §5 ok — oder eigene Begriffe? _______________
- [ ] Reihenfolge: **0 zuerst** (empfohlen) ODER **0+1 mergen**? _______________
- [ ] Phase 0 jetzt starten?
