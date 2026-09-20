# Pitfalls actually hit

Measured, not theorised. Each is symptom → cause → rule.

## Go, modules, git

- **`go test ./...` from the root does NOT test the agentkit modules.** `./...` is scoped to the
  current module; `go.work` only makes the others RESOLVE. A change to an agentkit interface can be
  green here and red in CI, which runs each module separately. Loop the modules before pushing
  (`.agents/docs/workflow.md`).
- **A nested `go.mod` needs `GOWORK=off`.** `internal/onnx/reference/` is its own module so gomlx
  stays out of nocturn's graph; `go.work` does not cover it, so a plain build inside it fails with
  "directory prefix . does not contain modules listed in go.work".
- **`go mod tidy` removes deps nobody imports yet** — add the import first, then `go get` + `tidy`.
- **Anchor `.gitignore` rules at the root** (`/plugins/`, `/workspaces/`), otherwise `plugins/`
  matches every directory of that name at any depth. `internal/workspace/` was accidentally ignored
  once and never committed — HEAD did not build from a fresh clone.
- **`git mv a/x b/x` nests when `b/x` already exists** — a leftover directory holding only a
  `.DS_Store` is enough, and you silently get `b/x/x`. Check for leftovers before a bulk move.
- **gopls diagnostics can be stale — the compiler is the truth.** "undefined: X" with a green
  `go build ./...` = stale index. Never restructure on suspicion; build first.
- **`.env` is gitignored**; `godotenv.Load()` reads from CWD and real env vars win.

## Workspaces and secrets

- **Renaming a workspace folder destroys its credentials, silently.** The folder name is the input to
  `Master.WorkspaceKey` and to `Master.ShardKey` for every shard, with the workspace-relative path
  bound in as AAD. A rename produces no error at all until something reaches for a credential, and
  then the failure appears somewhere else as an absent token. The title in `workspace.json` is what
  changes (ADR-16).

## wazero

- **`Memory.Read` returns a view, not a copy.** Copy the bytes out immediately before the host
  function returns; never hold them past the call (`memory.grow` reallocates). Within one host call
  the guest is suspended → race-free. One instance = one goroutine.

## Models and inference

- **LLMs are never fireproof.** Robustness = structured `tool_calls` + schema as a guardrail + our own
  argument validation and retry, with the error fed back to the model.
- **A model's documented default is not the default it was trained with.** `internal/speaker`
  implemented Kaldi's filterbank from the specification and used Kaldi's own default window, `povey`;
  WeSpeaker overrides it to `hamming`. All nine property tests passed — window shape, tone in the
  right Mel bin, gain absorbed — and same-speaker similarity sat at 0.73 instead of 0.98. Read the
  training code and pin the frontend against the implementation, not against a reading of it.

## go-tui and `.gsx` (`internal/tui`)

- **A computed `class={…}` is silently dropped.** Tailwind classes are resolved by the generator at
  build time, so only a string LITERAL becomes layout options. `class={"flex-col grow " + f()}`
  compiles, generates, runs — and produces an element with no layout at all, which reads as a layout
  bug rather than a dropped attribute. Anything that varies goes through a typed attribute
  (`borderStyle`, `width`, `padding`). Same trap for indentation: `pl-N` built from a depth is
  computed, so nesting is drawn with a sized spacer element.
- **A `ref` inside a PURE `templ` points at a discarded element.** A pure templ builds its element in
  its constructor; on a re-render `Mount` calls the factory again but renders the CACHED one, so the
  ref is rebound every frame to an element that is not in the tree. Only the first frame works, which
  reads as "clicking does nothing". Refs belong on elements built inside a STRUCT component's
  `Render`. Inside a loop, `ref={m} key={k}` generates `RefMap.Put(k, el)` — that is the intended
  form. Clicking is ref + `ContainsPoint` and nothing else; there is no event target on a
  `MouseEvent` and no hover at all (`MouseAction` is Press/Release/Drag).
- **The wheel IS built in; the scrollbar is not.** An unconsumed `MouseEvent` falls through to hit
  testing and `handleScrollEvent` scrolls by ONE line. Our panes intercept it: three lines is the
  readable step, and — load-bearing — a pane re-asserts `scrollOffset={…}` every render, so a scroll
  the framework writes onto `e.scrollY` is overwritten by the next frame. The bar itself is drawn and
  never read; clicking and dragging it is ours (`onScrollbar` / `scrollToPoint` in `pane.gsx`),
  derived from `ContentRect()` and `MaxScroll()`. The thumb's height is deliberately NOT re-derived.
- **`Element.ContainsPoint` is wrong for a CHILD of a bordered or padded container.** A child's rect is
  measured from its scroll container's content box starting at zero; ContainsPoint converts a screen
  point by adding every scrollable ancestor's scroll offset and never subtracts where that content box
  begins. Top-level elements are fine, but a line inside a pane is off by the border, so a click lands
  one row down and misses the last visible row. Subtract the container's `ContentRect()` origin from
  the point first and let ContainsPoint add the scroll (`internal/tui/tool.go:toolAt`, pinned by
  `TestHitTestingAChildOfAScrolledPane`).
- **A trapping `<modal>` ends its KeyMap with `OnPreemptStop(AnyKey)`.** go-tui dispatches in three
  passes: focus-gated stop handlers, then preempt, then normal. A key meant to close an overlay has to
  be `OnPreemptStop` on the ROOT; an ordinary `OnStop` never runs. `Modal` implements no
  `PropsUpdater`, so a cached modal keeps the KeyMap its factory produced the first time — which is
  why every overlay's keys live on the root, rebuilt per render.
- **`Ctrl+C` is a keystroke, not a signal.** The framework clears `ISIG`, so it never reaches the
  process as SIGINT and the app decides what it means — here: cancel the turn, do not quit. A handler
  that ignores `ctx` leaves the asking goroutine blocked forever, which is what the old terminal
  approver did.
- **A `.gsx` component whose whole body is inside an `if` returns nil**, and the framework
  dereferences what Render returns — a nil-pointer panic on the first frame. Keep the outer element
  unconditional. Also: `<markdown>`, `<input>`, `<textarea>` and `<modal>` only work inside a STRUCT
  component, and the generator's parser chokes on an unnamed `struct{}` parameter — which is why the
  logic lives in `app.go` and the `.gsx` holds only the shape.
