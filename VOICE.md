# Voice — open items

What the live-voice work knows it has not done yet. Design lives in
`docs/src/content/docs/architecture/live-voice.md`; this is the list of loose ends.

---

## 1. The satellite's front end is configured for the wrong job

`satellite/main/speech_det_driver/mic_speech.c` builds the audio front end as:

```c
afe_config_init(esp_get_input_format(), models, AFE_TYPE_SR, AFE_MODE_LOW_COST);
```

Both arguments are inherited from the vendor demo and neither fits a satellite.

**`AFE_TYPE_SR` vs `AFE_TYPE_VC`.** From esp-sr's own header:

```c
AFE_TYPE_SR = 0, // Speech recognition scenarios, excluding nonlinear noise suppression
AFE_TYPE_VC = 1, // Voice communication scenarios, 16KHz input, including nonlinear noise suppression
```

Our audio is carried to a remote listener, which is voice communication, not local recognition —
so `ns_init = true` may currently be doing nothing, and the noise the far side hears is noise we
could have removed. Everything after the wake word is a communication problem.

**`AFE_MODE_LOW_COST` vs `AFE_MODE_HIGH_PERF`.** esp-sr's benchmark distinguishes
`AEC(SR_LOW_COST)` from `AEC(FD_HIGH_PERF)`. Echo cancellation quality is the property the whole
satellite rests on: it is what lets the device listen while it speaks, and therefore what makes
barge-in possible at all.

**The blocker:** whether WakeNet runs under `AFE_TYPE_VC` at all. esp-sr's benchmark table only
shows WakeNet in SR configurations. If it does not, the two cannot simply be swapped and the shape
has to change — for example SR until the wake word, VC for the utterance, which is a bigger job than
changing an argument.

**How to decide it:** measure, do not read. Same room, same sentence, same distance, with the
capture-and-replay build; compare what comes back under each combination. The board reports peak
level per heartbeat, so "quieter" and "cleaner" can be separated.

---

## 2. Gain is not attributed

AGC and a 3× linear output gain were switched on in the same change, so it is not known which made
the difference:

```c
cfg->agc_init = true;
cfg->agc_mode = AFE_AGC_MODE_WEBRTC;
cfg->afe_linear_gain = 3.0f;
```

This matters beyond tidiness. A fixed multiplier clips in a loud room, where AGC would back off —
so if AGC alone is enough, the multiplier is a liability rather than a help. Set
`afe_linear_gain = 1.0` and read the peak.

---

## 3. Daemon-side, deferred on purpose

- **`goAway` ends the session.** Connection lifetime is roughly ten minutes regardless of health,
  and the documented response is to reconnect with a session-resumption handle rather than treat it
  as failure. Accepted for now; it will bite the first long conversation.
- **Transcripts are not persisted.** `voice.Driver.Run` returns them and the harness logs them.
  Wiring them into the chat store is what makes a spoken session continuable by typing — and is also
  when the flush-ordering question becomes real, since transcription frames carry no ordering
  guarantee against `serverContent`.
- **The redirect double-approval is invisible.** One `http_read` can ask twice when a host redirects
  elsewhere, and nothing in the log distinguishes the second ask from a retry. A `hop=redirect` field
  in `internal/tools/net.go`'s `checkRedirect` would have saved an hour.

---

## 4. Before a satellite is on the network

Not optional, and not yet done. The harness is loopback-only and unauthenticated, which is safe for
exactly that reason. A satellite is on the LAN, so it needs a bearer — and a device class that
cannot answer approvals. The approval broker takes the first answer it receives, so a device with a
microphone and no authenticated input would outrace the phone it exists to defer to.
