# The satellite's state machine

A screenless device has one output: a ring of seven LEDs. Everything the person can know about what
the system is doing has to fit through it. This is what the states are, who owns them, how the
daemon's knowledge reaches them, and what each one looks like.

---

## 1. The two axes, and why there is still one enum

Two independent facts describe the device at any moment:

- **Reachability** — is there a link to the daemon at all
- **Conversation** — is anyone listening, thinking, or talking

They are orthogonal: a device can be mid-utterance when the router reboots. But the ring can only
show one thing, so the states are a single enum and reachability **overrides** conversation. That is
not a simplification, it is the truth: a device with no link cannot listen, whatever it was doing a
moment ago.

## 2. The states

| State | Means | Entered from |
|---|---|---|
| `BOOT` | powering up, before WiFi | reset |
| `NO_NETWORK` | not associated, or associated without an address | any state, when WiFi drops |
| `NO_DAEMON` | on the network, but no socket — daemon down, or discovery still looking | any state, when the socket drops |
| `FAULT` | will not recover without a human | any state |
| `IDLE` | link is up, waiting for the wake word | `NO_DAEMON` on connect, `LISTENING`/`SPEAKING` on session end |
| `LISTENING` | a session is open and the person may talk | `IDLE` on wake word, `SPEAKING` on barge-in, `THINKING`/`SPEAKING` on turn done |
| `THINKING` | the person stopped, nothing has come back yet | `LISTENING` on end of utterance |
| `SPEAKING` | model speech is playing | `THINKING` on first audio frame |
| `APPROVAL` | a tool is waiting for a human decision on another device | `THINKING`/`SPEAKING` when the gate asks |

**`NO_NETWORK` and `NO_DAEMON` are separate, and the reason is repair.** Both look like "it does not
answer", but they send the person to different places: one is the router or the antenna, the other
is the machine running nocturn. The device knows which — `wifi_connected()` and `link_connected()`
are independent — so collapsing them would be throwing away the one piece of diagnosis it can
actually make. With RSSI measured at −88 dBm on this board, the distinction is not hypothetical.

**`FAULT` has three causes and one appearance.** Not provisioned (`provision_load` failed), token
rejected by the daemon (close code 4401), or the front end's detect loop having exited — after which
the board is deaf while looking perfectly healthy. All three mean "a person must come", the console
says which, and the ring does not try to spell out three different repairs in one colour.

**Barge-in is a transition, not a state.** It is the edge `SPEAKING → LISTENING`, and giving it a
state of its own would mean a state the machine leaves on the next tick — which is a visual effect
wearing a state's clothes. It gets one: a brief flash on the transition, so the person sees that the
interruption registered.

**`connected` is likewise not a state.** Being connected is the precondition for `IDLE`, not a thing
to sit in. The moment of connecting is worth showing, so `NO_DAEMON → IDLE` gets its own brief
effect.

`APPROVAL` is not in the usual list of voice-assistant states and is the one nocturn cannot do
without. The whole design rests on a person approving an action out of band; a device that gives no
sign that it is waiting for that leaves them staring at a speaker that has gone quiet for no visible
reason.

## 3. Who owns the state

**The board owns it. The daemon supplies facts.**

This is the load-bearing decision and it follows from latency. Every state the person can perceive
as slow — the ring lighting up when they speak, the interruption registering — must be decided from
what the board itself observed. A round trip to the daemon is 150–250 ms on a LAN and the threshold
where an interaction stops feeling immediate is around 200 ms.

Detection is **not** free, and the budget has to be honest about it. The front end's detector cannot
trigger on the first frame — Espressif documents 1 to 3 frames of inherent delay — and on top of that
it only reports speech once `vad_min_speech_ms` of it has held, which is 128 ms here. So
`MIC_EVT_VOICE` arrives roughly 160–220 ms after the person actually started. Local detection does
not avoid that cost; it avoids *adding* a round trip to it. Going through the daemon would mean
310–470 ms, which is not an interruption any more.

That is also why `vad_min_speech_ms` is not something to shorten casually: it is the false-trigger
guard, and lowering it trades a faster barge-in for a device that interrupts the assistant on a
cough.

So the split is by *who can know first*:

| Owned by the board (local, immediate) | Owned by the daemon (only it can know) |
|---|---|
| `NO_NETWORK`, `NO_DAEMON`, `FAULT` — it holds the radio and the socket | first model audio → `SPEAKING` |
| wake word → `LISTENING` | turn finished → back to `LISTENING` |
| end of utterance → `THINKING` | the gate is asking → `APPROVAL` |
| voice while speaking → `LISTENING` (barge-in) | session-level failure → `FAULT` |

A daemon message that contradicts what the board just observed **loses**. If the daemon says
`speaking` while local VAD says a person is talking, the person wins — they are in the room and the
daemon is not.

## 4. The state machine suppresses its own noise

`MIC_EVT_VOICE` means "the front end heard a voice". It does not mean "a person is interrupting",
and the gap between those two is the state machine's to close — it is the only party that knows what
the device itself was doing at that moment.

There is one such case and it is measured. Raising the speaker amplifier **clicks**, the microphone
hears the click, and the detector reports it as a voice. Exactly one spurious detection per playback,
at the start, independent of how long the playback ran — a five second replay triggered as often as
a three second one, with the microphone silent in between.

Nothing in the audio path can remove it. The echo canceller subtracts the playback signal it is
handed *digitally*; the click happens *after* the DAC, inside the amplifier. Digitally there is
silence at that instant, so there is nothing to subtract, and NLP, BSS and detector tuning all
operate on what the canceller leaves behind. Confirmed by trying: `vad_mute_playback` changed
nothing, one trigger per playback with it and without.

Two fixes were tried and rejected before the right one:

| Tried | Why not |
|---|---|
| Hold the amplifier up permanently | Removes the click, but an amplifier held on hisses audibly into a quiet room. Heard on hardware, reverted. |
| Make the detector less sensitive | Blunts it for real speech too, which is the one thing it must stay sharp for. |

**So: `SPEAKING` ignores `MIC_EVT_VOICE` for its first ~200 ms.** The state machine raised the
amplifier, so the state machine discards what the amplifier caused. It costs nothing real — the
assistant has barely started its first word, and nobody interrupts a sentence that has not begun.

The general rule this is an instance of: **a self-inflicted observation is not evidence.** Any future
action of the device's own that the microphone can hear belongs in this same place, not in the front
end's configuration.

## 5. Where the states come from

Everything the ring shows comes from five sources, and all five already sit on ESP-IDF's event loop
or can post to it. **Nothing is polled.**

| Source | Signal | Arrives as |
|---|---|---|
| `wifi` | `WIFI_EVENT_STA_DISCONNECTED`, `IP_EVENT_STA_GOT_IP` | already handled in `on_wifi` |
| `link` | `WEBSOCKET_EVENT_CONNECTED` / `_DISCONNECTED` / close 4401 | already handled in `on_event` |
| `mic_speech` | wake word, `MIC_EVT_VOICE`, end of utterance, detect loop exited | its own callback |
| `provision` | not provisioned | once, at startup |
| daemon | `voice.state` | `link_control_cb` |

An earlier draft of this document polled `wifi_connected()` and `link_connected()` on the renderer's
tick, on the reasoning that network conditions have no deadline. That reasoning was fine and the
conclusion was wrong, for two reasons.

**The handlers already exist.** `wifi.c` registers `on_wifi` against `WIFI_EVENT` and `IP_EVENT`;
`link.c` registers `on_event` for the whole WebSocket set. Both already receive exactly the
transitions in question and simply keep them to themselves — one sets an event-group bit, the other
a `bool`. There was never any callback plumbing to avoid; the plumbing is built and the state module
is the first thing that wants to listen.

**Polling loses transitions.** A socket that drops and reconnects inside one tick is invisible to a
poll and leaves the ring showing a link that briefly did not exist. Events cannot miss it. For a
device whose whole job is to be honest about its own state, "we sampled and it looked fine" is the
wrong default.

### One event base for the board's own signals

The two network sources post system events already. For the board's own — wake word, voice activity,
end of utterance, front end dead — declare a base and post to the same loop:

```c
ESP_EVENT_DEFINE_BASE(SAT_EVENT);
enum { SAT_WAKE, SAT_VOICE, SAT_UTTERANCE_END, SAT_MIC_DEAD, SAT_REMOTE_STATE };
```

Three things fall out of using the default loop rather than direct callbacks:

- **The state module runs on neither hot task.** `mic_speech`'s callbacks fire on the front end's
  fetch loop, where blocking starves echo cancellation, and the header says so in as many words.
  `link`'s fire on the WebSocket task, where blocking drops the connection. Posting an event is
  bounded and non-blocking; both callers stay as trivial as they are required to be.
- **One place to look.** Every input to the state machine arrives through one handler in one file,
  instead of four modules each holding a function pointer into a fifth.
- **`wifi.c` and `link.c` do not learn about states.** They post what happened. What it means is not
  theirs to know.

The loop's task runs at priority 20 with a 2.3 KB stack by default — well above the audio tasks and
ample for deciding a colour. It is created already, by `esp_event_loop_create_default()` in
`wifi_start`; the only ordering constraint is that the state module registers before the sources
start posting.

### The one thing an event cannot tell you

A task that **exits** can post before it goes. A task that **hangs** posts nothing, and no event
will ever arrive to say so.

`detect_task` is already covered for the hanging case: it is registered with the task watchdog and
calls `esp_task_wdt_reset()` each pass, so a stall reboots the board — loud, and correct. The exit
case is the gap. It breaks its loop when `afe_handle->fetch` fails, logs `detect loop exited`, and
deletes itself, and nothing notices. The board goes deaf — no wake word, no voice activity, ever
again — while the ring keeps showing `IDLE`, the link stays up, and the heartbeat keeps printing
`alive`.

That is the worst failure this device can have: broken, and indistinguishable from healthy. It posts
`SAT_MIC_DEAD` on the way out and reaches `FAULT` like any other unrecoverable state.

### Precedence

Sources disagree, so the order is fixed and total. Highest wins:

1. `FAULT` — nothing else matters if the device cannot do its job
2. `NO_NETWORK`
3. `NO_DAEMON`
4. `APPROVAL` — a person is being waited on, and that outranks whatever the conversation was doing
5. the conversation states: `LISTENING`, `THINKING`, `SPEAKING`
6. `IDLE`
7. `BOOT` — only before anything else has been established

Reachability above conversation is not a preference: a device with no socket cannot be listening,
whatever it was doing a moment ago, and showing green at that point is a lie.

`APPROVAL` above the conversation states is the one that is a judgement. It is there because an
approval that goes unnoticed stalls everything behind it, and because the person's attention needs
to move to another device — which is exactly when the ring must stop describing this one.

## 6. The wire

Existing convention, unchanged: commands up are `{"cmd": ...}`, everything down is `{"type": ...}`.
Both are WebSocket **text** frames; audio is binary in both directions. No new endpoint, no header
bytes.

**Board → daemon**

| Message | When |
|---|---|
| `{"cmd":"voice.wake","ws":"main"}` | wake word — exists |
| `{"cmd":"voice.end","ws":"main"}` | utterance over — exists |
| `{"cmd":"voice.barge","ws":"main"}` | **new** — voice detected while model speech was playing. The board has already flushed and already switched to `LISTENING`; this tells the daemon so it can stop generating. |

**Daemon → board** — one message, carrying the state the daemon believes the conversation is in.
`voice.state` already exists with `listening | idle`; it grows the rest of the set:

```json
{"type":"voice.state","ws":"main","state":"thinking|speaking|approval|listening|idle"}
```

Plus the existing `{"type":"voice.interrupt"}`, which stays what it is — a command to drop buffered
audio, not a state.

One message rather than a stream of events, because the board does not need the daemon's history,
only its current belief. A dropped state message is corrected by the next one; a dropped event in a
sequence desynchronises until the session ends.

## 7. Mapping the live events

`agentkit.LiveEvent` (`agentkit/live.go`) is the daemon's raw input. Most of it never becomes a
state:

| Live event | Becomes |
|---|---|
| `LiveAudio` (first of a turn) | `state: speaking` |
| `LiveAudio` (subsequent) | nothing — already speaking |
| `LiveTurnDone` | `state: listening` |
| `LiveInterrupted` | confirms a barge-in; `state: listening` if the board is not already there |
| `LiveToolCall` | `state: thinking` — the model stopped talking to go do something |
| `LiveUserText`, `LiveModelText` | nothing on the ring; transcript only |
| `LiveCallsCancelled` | nothing |
| `LiveError` | `state: idle`, plus the session ends — the board shows `FAULT` only if the link itself is gone |
| gate asks a human (`hitl`, not a live event) | `state: approval` |

`LiveToolCall → thinking` is worth stating because it is the difference between a device that looks
broken and one that looks busy: the model goes quiet while a tool runs, and without this the ring
would sit on `speaking` through a silence.

## 8. What the ring shows

The renderer exists (`rgb_led_driver`): one task owning the strip, a 50 ms tick, and a
(pattern, colour) pair set atomically. Seven patterns, RGB triples, plus `rgb_flash` for moments.

Tempo is part of the pattern rather than a parameter, because it carries meaning. A slow breath is
patience — the device is fine, waiting on something that is not you. A normal breath is an
invitation — it is waiting on YOU. Across a room the tempo is the only thing separating them, so it
cannot be a knob someone passes by accident. Same for the two blinks: slow wants repair, quick wants
a decision.

| State | Pattern | Colour | Reasoning |
|---|---|---|---|
| `BOOT` | `BREATHE_SLOW` | white | alive, nothing decided, nothing expected of anyone |
| `NO_NETWORK` | `BREATHE_SLOW` | amber | cut off from everything, waiting it out |
| `NO_DAEMON` | `SPIN` | amber | on the network and actively looking — discovery is genuinely running |
| `FAULT` | `BLINK_SLOW` | red | needs a person; the blink is what separates it from patience |
| `IDLE` | `SOLID` | dim blue | present without lighting a room at night |
| `LISTENING` | `BREATHE` | green | the one state where the person should act |
| `THINKING` | `SPIN` | blue | motion means working on it |
| `SPEAKING` | `WAVE` | cyan | reads as a voice, and is not green — the person should not feel invited to talk |
| `APPROVAL` | `BLINK` | magenta | the only magenta and the only quick blink: *go look at your phone* |
| barge-in edge | `rgb_flash` | white | ~150 ms, then `LISTENING` |
| connect edge | `rgb_flash` | green | ~150 ms, then `IDLE` |

Two pairs share a pattern and are told apart by colour, which is deliberate rather than a shortage:
`SPIN` means *working on it* in both cases — amber is looking for the daemon, blue is the model
thinking — and `BLINK` means *a person is needed* in both, red to repair and magenta to decide.
Learning two shapes is cheaper than learning nine.

Green appears exactly once, on `LISTENING`. The single question the person asks the ring is *may I
talk now*, and one colour should answer it.

Two details that only showed up on hardware: a breath must not reach zero, or it reads as a blink
with a long pause; and the crest in `WAVE` sits on a floor so every pixel stays lit, which is what
makes it one moving thing rather than several blinking ones — the difference from `SPIN` at a
glance.

## 9. Dumb drivers, one brain

The rule: **a driver reports what happened and executes what it is told. The state machine decides.**
Nothing else holds state that describes the conversation.

This is not tidiness. It was arrived at through a failure that could not have happened otherwise.

### The bug that made the case

`mic_speech` ran a session state machine of its own — `session`, `held`, `voice`, a silence timer —
next to the one in `state/`. The wake word is suppressed while a session runs, and it was re-armed in
exactly one place: the branch where a session ends on silence. Holding the stream open for a whole
conversation is precisely what stops it ever reaching that branch, so releasing the hold left the
session set and the wake word off. Permanently.

The board went deaf after its first conversation, reported `peak=0`, and looked healthy. Neither
machine was wrong on its own; they simply did not know about each other.

### Where state lives today

| Module | State | Decisions it makes |
|---|---|---|
| `mic_speech` | `session`, `held`, `voice`, `silence_ms` | when an utterance ends, whether to stream, when the wake word is armed |
| `main.c` | `hand_recording`, `confirmed`, `playing_back` | uplink open/closed, amplifier up/down, what `voice.state idle` means |
| `audio_out` | `playing`, `refill`, `freed` | when playback starts |
| `state/` | the machine | — and it can reach none of the above |

### Where it goes

**`mic_speech` becomes a detector.** It emits PCM and two edges — wake word heard, voice began — and
takes two commands:

```c
void mic_arm(bool);     // is the wake word listening
void mic_stream(bool);  // does PCM leave this module
```

Gone: `session`, the silence timer, `SILENCE_TO_END_MS`. When an utterance ended is a conversation
question, not a signal-processing one, and the module that answers it must be the one that also knows
whether a reply is playing.

**`audio_out` keeps its playout logic and loses nothing.** The prebuffer, the drain clock and the
credit accounting are the mechanics of getting samples to a codec — they are the driver's job, not
policy. It gains no state.

**`uplink` becomes `uplink_enable(bool)`** and stops calling anything a session.

**`state/` gains the actuators.** It already holds the facts and the precedence; the transitions now
also carry the commands — arm the wake word on the way into `IDLE`, raise the amplifier on the way
into `SPEAKING`, open the uplink for the length of a conversation. What is currently an invisible
side effect somewhere becomes a line in a transition.

**`main.c` becomes composition plus the bench button.** No `confirmed`, no `playing_back` as control
state.

The test that this worked: the wake-word bug becomes impossible to write. "Arm the wake word" is a
line in the transition into `IDLE`, not a consequence of a timer in a driver.

## 10. The model's state has to reach the ring

`THINKING` and `APPROVAL` are in the enum, in the ring mapping and in the protocol — and **nothing
ever produces them**. `internal/serve/voice.go` sends exactly two states, `listening` and `idle`, and
those are the only two `VoiceState` literals in the tree. `SPEAKING` works only because the board
derives it locally from its own amplifier.

So the ring cannot show that the model is composing, that a tool is running, or that an approval is
waiting on a phone — the last of which is the one nocturn most needs, since a person who is not told
to look at their phone will simply stand there.

The driver sees all of it: `LiveTurnDone`, `LiveToolCall`, `LiveAudio`, and every approval passes
through its own `announcing` wrapper. It keeps all of it.

There is already a port: `voice.Observer`, with `Said` and `ToolRan`. It is never set — the workspace
passes only `WithSystem` and `WithLogger` — and it has no notion of state. It grows one:

```go
type Observer interface {
    Said(role agentkit.Role, text string)
    ToolRan(name, args, result string, err error)
    Turn(TurnState) // Composing | Speaking | AwaitingApproval | Done
}
```

Three layers, one job each, and the same rule as §3 all the way down:

| | Knows |
|---|---|
| driver | what the model is doing |
| `serve` | how to put that on a wire |
| `state/` | what a person should see of it |

The driver never learns that a ring exists; the board never learns that tool calls do.

`SPEAKING` stays derived from the local amplifier even so. The daemon's version arrives later and is
about generation rather than sound, and the suppression window in §4 covers a hardware event this
board causes — that cannot depend on a message.

## 11. Where it lives

A new module, `satellite/main/state/`, owning the enum, the precedence rule and the LED mapping.
Nothing else calls `rgb_show`.

`main.c` currently sets colours from four places with no notion of a state, which is why the ring was
already lying before any of this: an unprovisioned board and a board merely waiting for the network
were both solid red and indistinguishable.

```c
esp_err_t state_start(void);   // register the handlers; call before the sources start posting
sat_state_t state_get(void);   // for the heartbeat line
```

That is the whole surface. Everything else arrives as an event, so there is nothing to call.

Internally it holds a handful of facts — has an address, has a socket, front end alive, session open,
what the daemon last said — updated by the handler, and **derives** the state from them in precedence
order, top down, first match wins. Deriving rather than storing is what keeps a missed transition
from stranding the ring: there is no accumulated state to get out of step, only facts and a rule.

One timestamp is stored: when `SPEAKING` was entered, for the 200 ms suppression in §4.

## 12. Order of work, and the precondition

1. ~~**The pattern renderer** in `rgb_led_driver`.~~ **Done** — one owner, seven patterns, RGB
   triples, `rgb_flash`. It also removed a real defect: two tasks were calling `led_strip_refresh`
   concurrently, which enables and disables the RMT channel, so every boot lost its first colour and
   the demo's path had that refresh inside `ESP_ERROR_CHECK`.
2. **`state/` module** — enum, precedence, LED mapping, the 200 ms suppression, and one handler on
   the default event loop. `wifi.c` and `link.c` post from the handlers they already have;
   `mic_speech` posts its events plus `SAT_MIC_DEAD` on the way out of a failed fetch. At this point
   the board shows `BOOT`, `NO_NETWORK`, `NO_DAEMON`, `FAULT`, `IDLE` and `LISTENING` correctly, and
   the connect flash works — all with no protocol change at all.
3. **`voice.state` grows** on the daemon, and the board applies it: `THINKING`, `SPEAKING`,
   `APPROVAL`. This is where the ring stops being about the board and starts being about the
   conversation.
4. **`voice.barge`** and the local barge-in edge.

### The precondition for step 4, and where it stands

Local barge-in fires on `MIC_EVT_VOICE`, which runs on the echo-cancelled signal. If cancellation
does not hold, the board hears its own speaker and interrupts itself — which is what the full-duplex
build did, 226 ms into every reply (`AUDIO.md`).

`self_trig` in the heartbeat measures exactly that: voice detections while the speaker was playing,
during a replay where the only voice in the room is the board's own. Measured across three replays of
3.0 s, 5.0 s and 3.1 s:

| Configuration | Self-triggers per replay |
|---|---|
| `AFE_TYPE_SR` (before) | reply cut at 226 ms, every time |
| `AFE_TYPE_FD` + NLP + BSS + VADNet | **1**, always at the start |
| plus `vad_mute_playback` | 1 — no change |

The residue is one per playback, not one per second of playback, and the microphone reads `peak=0`
in between. That is the amplifier's switch-on click (§4), not echo. **Cancellation holds.** The
acoustics are not the blocker they were, and step 4 is unblocked once the 200 ms suppression in §4
exists — which is part of step 2, not a separate investigation.

What is still unmeasured: whether real speech is detected *during* playback, rather than merely not
falsely detected. Every test so far had the person silent through the replay. One run talking over it
answers it, and it should be done before step 4 rather than after.

Steps 1–3 do not depend on any of this.

### Two hardware findings from the same logs

Neither belongs to the state machine, both will bite it:

- **`led_strip_rmt: enable RMT channel failed`** at boot — the LED driver's RMT channel does not come
  up cleanly. Everything in §7 depends on it, so step 1 starts here.
- **WiFi RSSI −88 to −89 dBm.** That is at the edge of usable and explains a good share of the link
  drops and jitter blamed on the audio path. Worth fixing at the antenna or the access point before
  reading much into any latency number.
