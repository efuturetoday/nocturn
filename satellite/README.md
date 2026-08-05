# The satellite

A speaker in a room that you talk to. ESP32 firmware, ESP-IDF, about 4,000 lines of C over
41 tracked source files.

It is not a client in the usual sense. It holds no key you type, has no screen to show a pairing
code, and cannot approve anything — a hallway speaker that could authorise an action would defeat
the reason approvals leave the room in the first place. What it does is carry a microphone, a
speaker, and a ring of light that never lies about what the system is doing.

## The one contract with the daemon

There is no shared code. The whole coupling is the WebSocket protocol in `internal/serve` — tagged
JSON for control, raw binary frames for audio, which are the only frames on the socket that are not
commands. Anything that speaks it is a satellite.

Two rules make the ring honest, and both come from the same observation: **a round trip is slower
than a person's sense of immediacy.**

- **The board owns what it can see itself.** The falling edge of the voice detector, the amplifier
  actually running, playback finishing. `LISTENING → THINKING` is decided here, because the daemon
  cannot report it any sooner than the round trip the person is already waiting through.
- **The daemon owns what only it can see.** A session opening, going idle, and — the one state no
  device could infer — a tool waiting for a human decision on somebody's phone.

An `idle` that arrives while the speaker is still running is **held, not obeyed**. The model
stopping generating is not the audio stopping: seconds of it are still in the daemon's backlog, the
socket, the playout ring and the DMA. Only this board knows when the last sample is out, so only
this board ends the conversation.

Recording for voice enrolment is its own state, and while it runs the ring goes **steady red** — a
deliberate departure from the fault colour, which blinks. Somebody walking into the room should be
able to tell they are being recorded without knowing the palette.

## Layout

```
main/state/              the event-loop state machine — start here, it is the interesting part
main/net/                wifi · provisioning · mDNS discovery · uplink · link
main/speech_det_driver/  wake word and voice activity
main/audio_out/          playout ring, credit-based flow control
main/micbuf/             the microphone buffer feeding the uplink
main/rgb_led_driver/     the ring
main/button/  main/tca9555_driver/  main/hardeware_driver/   board I/O
main/bench/              on-board timing, for the numbers in the comments
tools/ring.html          a browser simulator of the ring, to design the states against
```

Nothing is polled. Every state change arrives as an event on this module's own loop: a socket that
drops and reconnects between two samples is invisible to a poll, and would leave the ring showing a
link that briefly did not exist.

## Building and flashing

```bash
./build.sh                 # ESP-IDF in Espressif's container — the toolchain is not installed on the host
./flash.sh                 # build, flash, and open the monitor
./provision.sh             # write this board's identity and daemon address into NVS
```

The toolchain stays in a container on purpose: ESP-IDF wants its own Python virtualenv and roughly
2 GB of cross-compiler. There is a native arm64 image, so nothing is emulated.

Provisioning exists because the join flow has nothing to work with here — no screen, no keyboard.
An already-paired device asks the daemon for a token on this board's behalf, and a device may only
enrol a class whose abilities its own class already covers. An appliance therefore enrols nothing,
which is what makes a stolen speaker a dead end.

`managed_components/` is fetched by ESP-IDF on the first build and is not committed; neither is
`sdkconfig`, `build/` or `.flash/`.
