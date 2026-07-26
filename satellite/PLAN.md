# Putting the satellite on the network

How the board stops replaying itself and starts talking to the daemon. Written after the audio path
was proven on hardware; the open questions that survive it live in `../VOICE.md`.

## What already exists

| | |
|---|---|
| Board | wake word, echo cancellation against a hardware reference channel, VAD, AGC, playback queue, LED states — verified on hardware |
| Board | **no** networking: no WiFi, NVS, mDNS or WebSocket code, and neither `espressif/mdns` nor `espressif/esp_websocket_client` is in `idf_component.yml` |
| Daemon | `voice.Driver`, the `Device` port, the Gemini adapter, the read-only cage, approvals mid-conversation |
| Daemon | device classes, `POST /devices`, and a connection that may not approve is handed no broker |
| Daemon | `/voice/ws` in `cmd/` — loopback-only and unauthenticated, a measuring harness, not an endpoint |

The seam is one function. `on_pcm` in `main.c` copies samples into a PSRAM buffer; everything else on
the board stays as it is.

---

## The shape

### One connection, and it is the one that already exists

Audio rides `/ws`, as binary frames beside the tagged JSON. There is no second endpoint and no second
socket.

`/ws` is already a multiplexer — one connection per device carrying chat, agents, approvals,
reminders and notifications. A satellite with its own endpoint would be the exception, and every rule
that currently hangs off "a device's connection" would need a second copy: attaching to the approval
broker, joining the hub, `UpdateLastUsed`, presence.

Two consequences fall out of this rather than being designed:

- **The daemon can speak on its own.** `notify` and a fired reminder already reach every connected
  device; a speaker in a hallway can simply say them. Without a standing connection the daemon has no
  target and this is impossible. It runs through the existing `NotifyKind` gate, so it inherits those
  rules instead of opening a second path into someone's room.
- **The app gets voice for free.** Voice is a domain on the protocol the phone already speaks —
  `voice.wake`, `voice.end` next to `chat.submit` and `agent.fire`. With a separate endpoint the app
  would need a second socket later to do the same thing.

The connection to nocturn stays open; the **Gemini session** is what opens on the wake word. Those
are different things, and only the second one bills per audio minute.

### The connection stays stateless

`conn` is documented as holding no session state — commands are id-addressed, and a device that drops
mid-turn finds the turn still running when it comes back. That property is load-bearing and voice
does not get to break it.

So a voice session lives where a chat session lives: in the workspace, in a manager, keyed by device.

```
conn ──▶ voice.wake                    → voice.Manager.Start(deviceID)
                                         (the Manager runs voice.Driver.Run)
Manager ──▶ audio, addressed to device → hub delivers to that one connection
conn ──▶ voice.end                     → voice.Manager.Stop(deviceID)
```

`voice.Manager` mirrors `chat.Manager`: `Start`/`Stop` by id, an `OnEvent`-style sink, its own
teardown. The connection owns nothing and routes.

This also turns "should a session survive a reconnect?" from an architectural accident into a line
in the manager with a reason next to it. The answer is no — audio with nobody to play it to has no
purpose — but it is now a decision rather than a consequence of where an object happened to hang.

### Delivery is addressed, not broadcast

The hub broadcasts today, which is right for chat: every device shows the same conversation. It is
wrong for audio — your phone should not play what the hallway speaker is hearing.

So `hub.send(deviceID, msg)` beside `hub.broadcast(msg)`, and `conn` carries its device id. That is
identity, not session state, and it is already available where the connection is accepted.

### Control and audio do not share a queue

`conn.out` is one channel, drained by one writer, and `trySend` drops when it is full. For JSON a
drop means the client resynchronises. Audio is a steady 25–50 frames a second, so mixing them means
**a chat event is lost because audio filled the buffer.**

Two channels, one writer, control preferred:

```go
select {
case msg := <-c.control: // JSON, never dropped
    ...
default:
    select {
    case msg := <-c.control:
    case pcm := <-c.audio: // dropped when full — a click, not lost state
    }
}
```

Loss stays isolated without doubling the connection.

**What one socket does not solve:** a failure on the voice path takes chat, approvals and
notifications down with it. That is the accepted cost.

### Decisions taken

**The daemon resamples.** Gemini emits 24 kHz, the board runs one clock at 16 kHz. Go has CPU to
spare; the ESP32's two cores are already carrying the audio front end, and a stall there is what
breaks echo cancellation.

**Full duplex from the start.** The capture-and-replay build never records and plays at once, because
a loopback feeds the echo canceller its own output. Against a real far side that constraint hides the
question that matters: the speaker plays Gemini while the microphone hears the person, and the
canceller has to separate them. That is the test the loopback could not perform.

---

## Phase 1 — the daemon side

Go only, testable with a WAV file and no hardware, so it is finished before anything is flashed.

- **`internal/voice`** gains a `Manager`: `Start(deviceID)`, `Stop(deviceID)`, an event sink. It owns
  the `Driver.Run` goroutine and the per-session `Device` view over whatever connection is currently
  addressed by that id.
- **`internal/serve`**: `voice.*` commands in the tagged protocol; binary frames on the read side;
  `hub.send`; the split `control`/`audio` channels; `conn` carries its device id.
- **`satelliteDevice`**, implementing `voice.Device`: `Recv` takes binary frames as they are (already
  16 kHz mono PCM16), `Play` **resamples 24 → 16 kHz** and sends one binary frame, `Interrupt` sends
  the one text control message. The driver calls `Play`/`Interrupt` from its event loop and `Recv`
  from its pump, so one writer and one reader — the contract `gemini.Transport` already documents.
- **mDNS**: `_nocturn._tcp` already advertises `TXT path=/ws`; nothing to add, because there is no
  second path.
- **The approval property, restated where it is now enforced.** An appliance gets no broker because
  `newConn` is handed nil. A voice session is started by the manager, not by `newConn`, so it must not
  become a second way to reach the broker.

**Verification:** a Go test client that opens `/ws` with a minted appliance bearer, sends
`voice.wake`, streams a WAV as binary frames, and asserts audio comes back — no board involved.

## Phase 2 — the board reaches the daemon

Prove the network without touching audio: a build that connects, logs, and does nothing else.

- **Provisioning, for now: NVS written at flash time.** The board is being flashed anyway;
  `nvs_partition_gen` builds an image from a CSV holding `wifi/ssid`, `wifi/pass` and
  `nocturn/bearer`, wrapped in a script so the bearer never lands in a source file. The bearer comes
  from `POST /devices`. BLE provisioning is the later answer, described in `../VOICE.md`.
- **New components:** `espressif/mdns`, `espressif/esp_websocket_client`.
- **New `main/net/`:** `provision.c` (read NVS, refuse to start without it), `wifi.c` (connect,
  reconnect with backoff), `discover.c` (browse `_nocturn._tcp.local.`, with a hardcoded override
  because mDNS is the least reliable part of anyone's network), `link.c` (the standing WebSocket:
  connect at boot, reconnect on drop, send frames, dispatch what arrives).

**Milestone:** the board logs that it is connected, and stays connected across an access point
restart.

## Phase 3 — swap the sink

- `on_pcm` pushes into an uplink ring; a transmit task drains it. Same shape as `audio_out`: a
  zero-wait push that **drops rather than blocks**, because the callback runs on the front end's fetch
  loop and that loop must never wait. Drops are counted and reported by the existing heartbeat.
- Incoming binary frames go to `audio_out_write`, which already works.
- `playing_back`, the `capture` buffer and `playback_task` go away. Full duplex has nothing to
  schedule.
- `MIC_EVT_AWAKE` sends `voice.wake` and raises the amplifier. Session end is a silence timer in
  **tens of seconds**, not the 900 ms utterance boundary — that boundary is about turns, and
  turn-taking belongs to the model now.

**Milestone:** wake word, ask something, hear the answer.

**Measure here:** whether the board sustains 25–50 frames a second while the front end runs. That is
a measurement, not a judgement, and it is the last thing that could invalidate the shape above.

## Phase 4 — the rest

- **Barge-in.** `audio_out_flush` empties the ring but leaves the codec's DMA buffer holding its last
  block, which keeps sounding — the held tone from the replay build. A flush therefore also needs the
  silence write and the amplifier drop that `playback_task` currently does by hand.
- **Spoken notifications.** The standing connection makes them possible; what produces the audio is
  a separate question (a short live session, or something cheaper).
- **LED states** across the whole cycle, including a distinct one for disconnected — a satellite that
  cannot reach the daemon has to say so without words.

---

## Why this order

Phase 1 needs no hardware, so it can be finished and tested properly first. Phase 2 proves the network
in isolation, where a failure is unambiguous. Only then does the audio path move, which is the step
where a mistake is hardest to attribute — and by then everything underneath it has been checked
separately.
