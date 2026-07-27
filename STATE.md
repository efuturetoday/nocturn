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
| `OFFLINE` | no link — WiFi down, daemon gone, or discovery still running | any state, on link loss |
| `FAULT` | will not recover without a human — not provisioned, or the daemon rejected this device's token (close code 4401) | any state |
| `IDLE` | link is up, waiting for the wake word | `OFFLINE` on connect, `LISTENING`/`SPEAKING` on session end |
| `LISTENING` | a session is open and the person may talk | `IDLE` on wake word, `SPEAKING` on barge-in, `THINKING`/`SPEAKING` on turn done |
| `THINKING` | the person stopped, nothing has come back yet | `LISTENING` on end of utterance |
| `SPEAKING` | model speech is playing | `THINKING` on first audio frame |
| `APPROVAL` | a tool is waiting for a human decision on another device | `THINKING`/`SPEAKING` when the gate asks |

**Barge-in is a transition, not a state.** It is the edge `SPEAKING → LISTENING`, and giving it a
state of its own would mean a state the machine leaves on the next tick — which is a visual effect
wearing a state's clothes. It gets one: a brief flash on the transition, so the person sees that the
interruption registered.

**`connected` is likewise not a state.** Being connected is the precondition for `IDLE`, not a thing
to sit in. The moment of connecting is worth showing, so `OFFLINE → IDLE` gets its own brief effect.

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
| `OFFLINE`, `FAULT` — it holds the socket | first model audio → `SPEAKING` |
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

## 5. The wire

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

## 6. Mapping the live events

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

## 7. What the ring shows

The current driver has four colours and one operation — `rgb_set_solid`, which writes all seven
pixels immediately (`rgb_led_driver.c:90`). That is enough for a state but not enough to distinguish
eight of them, and a static ring cannot say "working on it".

So the driver grows a **pattern renderer**: one task, a 50 ms tick, and a current (pattern, colour)
pair set atomically. Patterns are `SOLID`, `BREATHE`, `SPIN`, `WAVE`, `BLINK`, `FLASH_ONCE`. The
existing demo already contains a running-light and a scan (`_example_playing`) which the renderer can
absorb rather than reimplement.

Colours become RGB triples rather than the four-value enum — `led_strip_set_pixel` takes RGB already,
only the enum is narrow.

| State | Pattern | Colour | Reasoning |
|---|---|---|---|
| `BOOT` | `BREATHE` slow | white | alive, nothing decided yet |
| `OFFLINE` | `BREATHE` slow | amber | something is wrong but it is trying |
| `FAULT` | `BLINK` slow | red | needs a person; distinct from offline's patience |
| `IDLE` | `SOLID` very dim | blue | present without lighting a room at night |
| `LISTENING` | `BREATHE` | green | the one state where the person should act |
| `THINKING` | `SPIN` | blue | motion means working; the only spinning state |
| `SPEAKING` | `WAVE` | cyan | reads as a voice, and is not green — the person should not feel invited to talk |
| `APPROVAL` | `BLINK` | magenta | the only magenta and the only urgent blink; it means *go look at your phone* |
| barge-in edge | `FLASH_ONCE` white | — | 100 ms, then `LISTENING` |
| connect edge | `FLASH_ONCE` green | — | 100 ms, then `IDLE` |

Green appears exactly once, on `LISTENING`. That is deliberate: the single question the person asks
the ring is *may I talk now*, and one colour should answer it.

## 8. Where it lives

A new module, `satellite/main/state/`, owning the enum, the transition table, and the LED mapping.
Nothing else sets a colour.

Today `main.c` calls `rgb_set_solid` from three different places with no notion of a state, which is
why it is already inconsistent — `on_event` paints red on wake and green on end, and the network task
paints red for "not provisioned", so an unprovisioned board and a listening board look the same.

- `state_set(sat_state_t)` — from the board's own observations
- `state_remote(const char *state)` — from `voice.state`, subject to the precedence rule in §3
- `state_get()` — for the heartbeat log

The link layer reports up/down into it; `mic_speech` reports wake, end of utterance and
`MIC_EVT_VOICE`; the LED task is the only consumer.

## 9. Order of work, and the precondition

1. **The pattern renderer** in `rgb_led_driver` — RGB triples, the six patterns, a 50 ms task.
   Standalone and testable by cycling through the states with nothing else running.
2. **`state/` module** — enum, transitions, LED mapping. Move the three existing `rgb_set_solid`
   calls into it. At this point the board shows `BOOT`/`OFFLINE`/`FAULT`/`IDLE`/`LISTENING` correctly
   with no protocol change.
3. **`voice.state` grows** on the daemon, and the board applies it: `THINKING`, `SPEAKING`,
   `APPROVAL`.
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
