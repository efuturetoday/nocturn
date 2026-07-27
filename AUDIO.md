# The audio path

A design for the speech path between a live model and a satellite, replacing one that was arrived at
by patching symptoms. Written after measuring what the current one actually does.

---

## What is wrong

### The architectural error

The daemon meters speech out on a Go `time.Ticker` at 32 ms. The board consumes it at whatever rate
its codec crystal runs at. Nothing connects the two.

> *"A push pacer is never correct as the sole rate control. The receiver consumes at exactly the rate
> set by its crystal. If the sender emits at anything else, the difference integrates without bound;
> there is no equilibrium."*

There is no buffer size that fixes this, because the error accumulates. Every deep buffer only
changes how long it takes to fail.

The board already contains the correct clock and does not use it as one: `i2s_channel_write()`
blocks until the DMA accepts, so a task looping on it **is** a hardware-paced clock, exact to the
crystal by construction.

### What the measurements said

From `played=19/17620 dry=30` over a three-second window:

- 17,620 real samples + 30 × 1024 invented silence samples = 48,340 ≈ 3.02 s. The playback loop runs
  at exactly real time — it is codec-clocked and correct.
- **64 % of what the speaker emitted was silence the board made up.** That is the stutter. Not a
  buffer that is too small; a sender that is not sending.

### What the audit found

| | |
|---|---|
| `pacer.buf` | **unbounded** — the only unbounded allocation in the path. A long turn holds the entire remaining reply in RAM. |
| `conn.audio` | 48 frames = **1536 ms**, documented as "roughly a second" |
| `Manager.mic` | 48 × 64 ms = **3072 ms**, documented as "a second or so" |
| **Interrupt** | drops the pacer backlog but **not `conn.audio`** — so after a barge-in the board flushes, then receives up to 1.5 s of exactly the speech it was told to abandon, and plays it |
| WebSocket task | **not pinned**, despite a comment claiming core 0 — it can land on core 1 with the audio front end |
| `on_event` | blocks the front end's fetch loop for **≥530 ms** at every session end |
| `esp_audio_mono16` | mallocs and frees 8 KB every 64 ms, and expands 4× so the bus carries 128 kB/s to deliver 32 kB/s |
| `audio_out_silence` | unbounded retry loop, called from the WebSocket receive task |
| Timing | **nothing anywhere measures time.** Every number is a count over a 3 s window. `dry=30` cannot distinguish "the daemon under-sent" from "the link stalled" from "the clocks drifted" — three different faults with three different fixes. |

---

## The design

### Credit-based flow control

The board tells the daemon how much room it has. The daemon sends only that much. Nothing else
decides the rate.

```
board  ──▶ {"cmd":"voice.credit","bytes":16384}   at connect, and as playback frees space
daemon ──▶ binary frames, while credit remains — no timer, at line speed
board  ──▶ {"cmd":"voice.credit","bytes":4096}    each time a quarter of the buffer drains
```

Why this and not a better timer:

- **Overflow becomes structurally impossible.** The board never receives more than it can hold, so
  the drop path stops being load-bearing.
- **Drift stops mattering.** Credits are issued by playback, which is clocked by the crystal. The
  loop closes on the only real clock in the system.
- **The buffer fills at line speed, not at speaking speed.** The first sentence arrives as fast as
  the network allows, so playback starts immediately rather than after a timer has dribbled out a
  cushion.
- **The backlog stays where it can be discarded.** Audio held in the daemon is audio a barge-in can
  throw away; audio on the device is not, and audio in the codec's DMA is not recoverable at all.

Credit updates are batched — one message per quarter buffer, not per byte — so the control channel
carries a handful of messages a second rather than flooding.

### What must be bounded, and to what

Every number below is derived rather than chosen.

**Board playout buffer: 128 ms, refill floor 40 ms, ceiling 200 ms.**

Sized for TCP over WiFi, not for a LAN. The dominant term is not jitter: **TCP turns a lost segment
into a stall of at least one RTT**, and lwIP's minimum retransmit timeout is in the hundreds of
milliseconds. One retransmit costs more than all the jitter combined. WebRTC's own cold-start target
is 80 ms and its steady-state target is the 95th percentile of observed delay — 128 ms sits above
both with room for a single retransmit.

**Daemon backlog: bounded, and the bound is the model's turn.** It holds what the model produced and
has not yet been credited out. It needs a cap and a policy for exceeding it — dropping the oldest
audio is wrong (the reply becomes incoherent), so the cap should end the turn and say so.

**Uplink: 32 ms frames, matching the front end's chunk.** ESP-SR's echo canceller works in 32 ms
frames in this mode, and an uplink frame that is not a multiple of that forces a second re-buffering
stage with its own latency and its own bugs.

**Downlink: 20 ms frames.** Opus's default for the same reason — the point where header overhead
(~7 %) stops mattering and latency starts to.

### Drift needs no machinery

At 16 kHz, a 100 ppm mismatch — the worst case for two consumer crystals — is **6 ms per minute**.
It would take 13 minutes to drain a 80 ms buffer.

No utterance is 13 minutes long. And between utterances the buffer drains to empty and refills, so
drift **cannot accumulate past a single reply**. Ten seconds of speech at 100 ppm is one millisecond.

This removes the entire apparatus the literature would otherwise demand: no time-scale modification,
no asynchronous resampling, no clock servo. The silence between turns is the resynchronisation
point, and it is free.

Frame drop and insert stays available as a safety valve, for a pathologically long single reply. It
should never fire.

### Barge-in

Hybrid, because the two halves answer different questions.

**The board stops itself.** Its voice-activity detector already runs on every frame and already
reports on the same call that delivers echo-cancelled audio. Local detection is 50–100 ms; going via
the daemon is 150–250 ms on a LAN, and the target for barge-in feeling natural is under 200 ms
total. So the board flushes on its own VAD and tells the daemon afterwards.

**The daemon decides what it meant.** Whether that was an interruption or somebody saying "mhm" is a
semantic question the board cannot answer, and the daemon may resume.

**What has to be discarded, in order:** the daemon's backlog and the model's generation itself
(which otherwise keeps producing, and billing, into nothing); the bytes already in flight, which
cannot be recalled and are the reason to keep the in-flight window small; the board's playout
buffer; and the I2S DMA descriptors, which only `i2s_channel_disable()` can drop and which otherwise
play out regardless.

**The tail that cannot be cancelled** is roughly 30–60 ms: DMA descriptors, the codec's own filter
delay, and the flight time from speaker to microphone.

**The trap that matters more than the tail.** The echo canceller's reference must match what the
speaker actually emitted. Flush the playout buffer while the DMA keeps playing what was already
committed, and the reference and the microphone signal disagree — *precisely at the moment of
barge-in*, which is exactly when the canceller must be at its best. It then diverges, the
assistant's own voice leaks into the uplink, and the VAD may re-trigger on it.

This board takes its reference in hardware — the codec presents it as a channel alongside the
microphones — so it is aligned by construction and no software flush can desynchronise it. That is
worth stating explicitly, because it is the reason a whole class of failure does not apply here.

### Capture overflow must not be silent

Today an overflowing uplink ring drops frames and splices what remains. That is the one thing every
audio stack agrees you must not do — WASAPI, PortAudio and ALSA all raise an explicit discontinuity
rather than splice.

The acoustic harm is survivable: a splice is a transient, and a recogniser produces a substitution
around it. The timeline harm is not. Every downstream sample count is silently wrong from then on:
endpointing desynchronises, word timestamps skew, and anything aligning the microphone stream
against the playback stream is permanently off by the dropped amount.

So a drop becomes a **gap marker** — `{"cmd":"voice.gap","samples":N}` — and the daemon inserts
silence to keep the timeline exact. And the ring is sized so that overflow means the network is
gone, not that the board was briefly busy.

### Instrumentation

Nothing in the path measures time. Every number is a count over a three-second window, which is why
`dry=30` cannot distinguish a daemon that under-sent from a link that stalled from clocks that
drifted — three faults, three different fixes, one indistinguishable symptom.

Two numbers earn their place, and they are built with the credit loop rather than before it. A
baseline taken today would not be comparable, because credits change the regime entirely.

**Playout buffer depth** — minimum, mean and maximum over a window. It answers the only question
that matters at the speaker: how close did it come to empty? A count of underruns says one happened;
a minimum says whether the next one is imminent.

**Credit round-trip** — from the board freeing space to audio arriving because of it. This is the
control loop's latency, and it is the design's precondition: if the round-trip exceeds the buffer's
depth, the buffer starves no matter how deep it is. Without this number, 128 ms is another guess of
the same kind as the 150 ms it replaces.

Deliberately not measured:

- **Drift.** Credits absorb it by construction, so the number would not be acted on.
- **Time blocked in the codec write.** Already answered: 17,620 real plus 30 × 1024 invented samples
  is 3.02 s in a three-second window, so the playback loop is exactly real-time. The board is not the
  bottleneck.
- **Frames sent versus played.** Worth checking once while building; not worth carrying.

---

## Parameters

| | Value | Why |
|---|---|---|
| Downlink frame | 20 ms / 640 B | overhead ~7 %, standard for Opus-class voice |
| Uplink frame | 32 ms / 1024 B | matches the echo canceller's chunk exactly |
| Board playout buffer | 128 ms, floor 40, ceiling 200 | one TCP retransmit, not jitter |
| Credit grant | quarter buffer | a handful of messages a second |
| Daemon backlog | capped, turn ends on overflow | the only unbounded thing today |
| I2S DMA | `dma_frame_num=160`, `dma_desc_num=4` | 40 ms, 100 Hz interrupt — two orders below where boards fail |
| `auto_clear` | true | an underrun emits zeros instead of replaying stale samples |
| `TCP_NODELAY` | both ends | Nagle plus delayed ACK adds up to 40 ms of nothing |
| `WIFI_PS_NONE` | board | power save adds up to a beacon interval |

---

## Order of work

1. **Credit-based flow control, with its two measurements built in.** Removes the ticker, the
   overflow path and the drift question in one change, and reports whether it is working.
2. **Fix what the audit found** — the interrupt that does not drain `conn.audio`, the unpinned
   WebSocket task, the blocking in the fetch loop, the per-frame malloc.
3. **Local barge-in**, with the daemon arbitrating.
4. **Gap markers** on capture overflow.

Step 1 is the design. The rest are corrections that stand on their own.
