# Dev workflow

## Setup, once per clone

```bash
git config core.hooksPath .githooks   # the two commit gates (below)
cp .env.example .env                  # OPENAI_BASE_URL / _MODEL / _API_KEY
```

## The loop

```bash
# `./...` is scoped to the CURRENT module. go.work makes the agentkit modules RESOLVE from here; it
# does not put them in `./...`. Run each one, which is what CI does:
for m in . agentkit agentkit/{gate,openai,tools,runtime,gemini}; do (cd $m && go build ./... && go vet ./... && go test -race ./...); done

gofmt -l cmd internal agentkit        # must print nothing
(cd docs && npm run build)            # validates every tool and gate-kind YAML against its schema
```

## The two commit gates

`.githooks/` is the process, not a description of it, and it holds for every agent and for a human:

- **`pre-commit`** — staged `.go` files are refused until that exact diff has been through the Go
  review skills (`cc-skills-golang:golang-how-to` plus its style row). Fix the findings or justify
  each one you leave; the same staged content is never blocked twice (the stamp is
  `$GIT_DIR/go-review-stamp`).
- **`post-commit`** — a commit touching only `internal/` or `cmd/` reports what documentation it did
  not update. Update the affected docs, or say why none are needed.

Both live in `scripts/gates/`; `.githooks/*` and `.claude/settings.json` are two callers of the same
scripts. Working without either still obliges you to do both things.

## Running it

```bash
go run ./cmd/nocturn                  # the full-screen terminal chat (needs a TTY; exits 2 if piped)
go run ./cmd/nocturn serve            # the daemon: WebSocket + the browser UI at the same address
#   --host picks the interface ("" = all, 127.0.0.1 = this machine only, which also suppresses the
#   mDNS advert), --port the port, --no-web serves the protocol without the UI.
#   subcommands: auth <provider> · secret set|ls · mail setup|check · ls · pair · reload · version · help
```

TUI keys: `Ctrl+P` command palette (everything is in there) · `Tab` next region, named in the hint
line · `Enter` open/send · `Ctrl+C` cancel the TURN · `Ctrl+N` new · `Ctrl+K` workspace · `Ctrl+L` log
pane · `Ctrl+Q` quit · `j/k/PgUp/PgDn/g/G` scroll · `←→` filter the conversation list · click a tool
line for its whole input and output · workspace view: `1`-`7` open a section, `/` filters the three
long ones, `Esc` peels one layer.
In chat: `/new` `/open <id>` `/chats` `/agents` `/fire <name> <task>` `/help` `/quit`.
Diagnostics go to `nocturn-data/nocturn.log` — nothing prints while the UI owns the screen.

## Generators (all outputs committed, CI fails on drift)

```bash
# The browser UI is the SAME Angular bundle as the app, go:embed'd from internal/webui/dist —
# gitignored but for a .gitkeep, so a bare clone serves a "not built" page instead.
(cd mobile && npm ci && npm run build)
go generate ./internal/webui/         # copies dist/mobile/browser in; no bundle = says so, exits 0

# .gsx templates compile to committed *_gsx.go.
go install github.com/grindlemire/go-tui/cmd/tui@v0.18.2
tui generate ./internal/tui/...

wat2wasm internal/sandbox/testdata/echo.wat -o internal/sandbox/testdata/echo.wasm
internal/script/qjs/build.sh          # CI rebuilds and commits this too; by hand is just faster
```

## The published library catalog

Sources in `catalog/extensions/`, generated into `docs/public/catalog.json` (committed,
CI-drift-checked). `go test ./catalog/` installs every entry into a temp dir and reads each payload
back with its own loader, so something that could not be installed fails before it ships.

```bash
go generate ./catalog/
(cd catalog && go run sign.go entryread.go -keygen)        # mint a keypair (public half → library.signingKeys)
(cd catalog && go run sign.go entryread.go -key ~/key.txt) # sign every entry; the .sig is committed, CI never signs
(cd catalog && go run import.go notion.com)                # list/probe MCP-registry candidates to curate
```

Nothing UNSIGNED is published — the generator names the missing file and skips the entry, because a
remote daemon drops an unsigned one anyway. `NOCTURN_CATALOG_DEV_KEY` adds a trusted public key to one
daemon, for a locally signed plugin.

## Speaker recognition

The checkpoint (~26 MB) and the evaluation corpus are NOT committed; the tests needing them skip when
unset. Regenerating the two golden files and the measured thresholds:
`internal/{onnx,speaker}/testdata/README.md`.

```bash
export NOCTURN_SPEAKER_MODEL=…/wespeaker_en_voxceleb_resnet34.onnx
go test ./internal/speaker/ ./internal/onnx/
internal/speaker/reference/corpus.sh /tmp/corpus   # 40 LibriSpeech speakers, needs ffmpeg
NOCTURN_SPEAKER_CORPUS=/tmp/corpus go test ./internal/speaker/ -run Evaluate -v -timeout 30m
internal/speaker/reference/setup.sh   # torch venv, ONLY to regenerate the filterbank reference
```
