# The audio path

> **The path described here does not exist in the tree right now.** The satellite has been reset to
> the record-then-replay build of `804dc51` — wake word, five seconds of capture, replay through the
> speaker, and a WebSocket connection that is established but carries no audio. Full duplex is parked
> in `satellite/.fullduplex.patch`. This document is kept because its measurements and its account of
> what went wrong are what the rebuild has to start from.

The speech path between a live model and a satellite: what it does, and why it is shaped this way.

An earlier version of this document diagnosed the stutter as clock drift and proposed credit-based
flow control against a 128 ms playout buffer. That diagnosis was wrong and the design that followed
from it would have reproduced the failure it was meant to fix. What is below replaces it.

---

## The fault, and what it actually was

The board ran dry roughly two thirds of the time: `played=19/17620 dry=30` over three seconds is
17,620 real samples against 30 blocks of invented silence, adding to 3.02 s. The playback loop was
exactly real-time — it is codec-clocked, and correct. Speech was simply not arriving.

**The live model produces speech FASTER than real time.** Supply was therefore never the constraint,
which leaves exactly one candidate: the transport delivered below real time. It did, and the reason
was structural.

Flow control was TCP's: the board blocks in its WebSocket receive callback while its playout ring is
full, lwIP stops being drained, the receive window closes, and the daemon's write blocks. Sound in
principle. But the ring was **200 ms** and `CONFIG_LWIP_TCP_WND_DEFAULT` is 5760 B — **180 ms** of
audio. Refills happen only when the board frees space and lwIP sends a window update, which it does
once per couple of segments. So the whole pipe was ~380 ms and it was refilled one round trip at a
time: a stop-and-wait loop whose throughput is window ÷ round trip, and which starves outright on a
single lwIP retransmit (minimum timeout in the hundreds of milliseconds).

Playback started at a **40 ms** cushion, well under that round trip, so the ring was empty at every
refill boundary. The drain task then wrote a full block of silence each time it found nothing —
correct behaviour, since the write is what clocks the loop, but two thirds of the output was that
silence. That is the stutter.

### The mistake that was worse

Dropping the DMA on a barge-in means `i2s_channel_disable` on the TX half of a full-duplex pair, and
this board takes the echo canceller's reference from that same I2S instance. Cycling TX leaves the
reference no longer describing what the speaker emitted, so the canceller stops cancelling — and it
was cycled on **every wake**, since a session start flushes.

Measured on 2026-07-27 at 14:02:32: the model's first audio went out at `.593`, `live interrupted`
arrived at `.819`. 226 ms. What came back up the uplink was transcribed as ` بگید. <noise>` — the
board's own voice, mistaken for a person interrupting. Every reply died on its first word.

This is the trap described under *Barge-in* below, walked straight into. Ninety milliseconds of
uncancellable tail is far cheaper than a canceller that has diverged.

### The mistake behind the number

**Capacity is not latency.** What delays the first word is the fill level at which playback starts.
Capacity only decides how much jitter can be swallowed before the buffer runs dry. With a source that
outruns the speaker, a deep ring fills at line speed and then sits full — it costs nothing.

The 200 ms was chosen to keep a barge-in's reach short, on the belief that *audio already on a device
cannot be taken back*. It can. `audio_out_flush` empties the ring, and `esp_audio_drop_pending`
disables the I2S channel to discard the committed DMA descriptors. Both are local and immediate.

What actually survives an interrupt is fixed and independent of the ring:

| | |
|---|---|
| TCP in flight | ≤ the receive window, ~180 ms |
| I2S DMA descriptors | 6 × 240 frames, ~90 ms — dropped by disabling the channel |
| Codec filter delay + flight time speaker→microphone | 30–60 ms |

Roughly 300 ms with the DMA left alone, roughly 240 ms with it dropped — the same either way whether
the ring holds 128 ms or two seconds.

### Why drift needs no machinery

At 16 kHz a 100 ppm mismatch — worst case for two consumer crystals — is 6 ms per minute. Draining an
80 ms buffer would take thirteen minutes. No utterance is thirteen minutes long, and between
utterances the buffer empties and refills, so drift cannot accumulate past a single reply. The
silence between turns is a free resynchronisation point.

This removes the entire apparatus the literature would otherwise demand: no time-scale modification,
no asynchronous resampling, no clock servo, and no credit protocol to absorb a drift that is not
there.

---

## The design

**The daemon pushes. TCP stops it. The board buffers deep and flushes locally.**

```
model (faster than real time)
   │
   ├─ downsample 24 kHz → 16 kHz                      internal/serve/voice.go
   ├─ deviceSink.backlog        capped at 60 s, discarded on Interrupt
   ├─ conn.audio                48 frames of 20 ms
   ▼
TCP ── the daemon's write blocks when the board stops reading
   ▼
board  ring 2 s in PSRAM        blocking write in the receive callback IS the flow control
   ▼
drain task ── esp_codec_dev_write blocks on DMA, so the loop is crystal-clocked
   ▼
ES8311
```

Nothing meters, nothing paces, nothing counts credits. The only rate control in the system is the
codec accepting a write, which is the one clock that is real.

### Numbers, and where each comes from

| | Value | Why |
|---|---|---|
| Board playout ring | **2 s** (64,000 B, PSRAM) | capacity is free with a faster-than-real-time source; swallows retransmits |
| Prebuffer | **200 ms** (6,400 B) | the only latency the listener pays; must exceed one WiFi round trip |
| Ring write timeout | 5 s | blocking is the normal state; a return means playback is wedged |
| Downlink frame | 20 ms / 640 B | header overhead ~7 %, standard for Opus-class voice |
| Uplink frame | 32 ms / 1024 B | matches the echo canceller's chunk exactly |
| Codec scratch | 640 samples, static | was a malloc/free every 20 ms |
| `auto_clear` | **true** | an underrun emits zeros instead of replaying the last descriptor |
| `WIFI_PS_NONE` | set | modem sleep adds a beacon interval to every frame |

Reference points, all of which buffer far deeper than 200 ms and none of which run an application
flow-control protocol: ESPHome's `i2s_audio` speaker defaults to a 500 ms buffer and its media player
holds a 1 MB source ring; ESP32 streaming players (ESP32-audioI2S, snapclient) run 1–10 s in PSRAM.

### Barge-in

Three things hold speech and two are reachable.

1. **The daemon's backlog and the model's generation.** Discarded on `Interrupt`, which also drains
   `conn.audio` — otherwise the board flushes and is then handed the same speech again.
2. **The board's ring and DMA.** `voice.interrupt` triggers `audio_out_flush`; the drain task drops
   the DMA on its next pass and then waits for a fresh prebuffer.
3. **The bytes in the TCP window.** Not recallable. This is why `conn.audio` stays short.

The echo canceller's reference is taken in hardware on this board — the codec presents it as a
channel alongside the microphones — so a software flush cannot desynchronise it against the
microphone signal. That removes the failure mode that normally makes barge-in delicate.

---

## Still open

- **Local barge-in.** The board's VAD already runs on every frame; detecting locally is 50–100 ms
  against 150–250 ms via the daemon, and natural barge-in wants under 200 ms total. The board should
  flush on its own and tell the daemon afterwards; the daemon decides whether that was an
  interruption or somebody saying "mhm", and may resume.
- **Gap markers on capture overflow.** An overflowing uplink ring currently drops frames and splices
  what remains. The acoustic harm is survivable; the timeline harm is not — every downstream sample
  count is silently wrong from then on. `{"cmd":"voice.gap","samples":N}` and the daemon inserts
  silence to keep the timeline exact.
- **`on_event` blocks the front end's fetch loop** for ≥ 200 ms at every session end.
- **Instrumentation measures counts, not time.** `dry=30` cannot by itself separate a daemon that
  under-sent from a link that stalled. Playout depth (min/mean/max over a window) is the number worth
  carrying: a count of underruns says one happened, a minimum says how close the next one is.
