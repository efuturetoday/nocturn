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

## 4. Getting a satellite onto the network

The daemon side is **done** (`f43235d`): devices carry a class, `capabilitiesOf` in `internal/serve`
is the one place a class becomes authority, a connection that may not approve is handed no broker at
all, and `POST /devices` enrols under a single rule — a device may only enrol a class whose abilities
its own already covers. An appliance therefore enrols nothing and answers nothing.

What remains is getting a bearer onto the board.

### The principle

**A satellite must not enrol itself.** It has no screen and no keyboard: it cannot display a pairing
code or be handed one, so the `/join` flow has nothing to work with. And a device that can enrol
itself is not being authorised by anyone — that is the hole, not the feature.

So the phone does it. It is already paired and already authenticated, so it asks the daemon for a
bearer and passes it on. The human authorises on a device that *can* authorise; the satellite only
receives.

```
phone  ──▶ daemon:  POST /devices {name, class: "appliance"}
daemon ──▶ phone:   the bearer, once — afterwards only its hash exists
phone  ──▶ board:   wifi credentials + bearer          (BLE or SoftAP)
board:              into NVS
board  ──▶ mDNS:    find the daemon                    (the address is not provisioned)
```

### Now: write NVS at flash time

The board is being flashed anyway; `nvs_partition_gen` builds the image and the firmware reads
credentials and bearer from it. No provisioning code, no app flow, no BLE security design — and it
unblocks wifi and the WebSocket immediately. For one device in a flat this is proportionate; how it
scales is a question the second device asks.

### Later: BLE provisioning from the app

ESP-IDF ships `wifi_provisioning` (already in the component list) over BLE or SoftAP, with
Espressif's own apps to test against before ours can. It must run in security mode 2 (SRP) with a
proof of possession supplied out of band — printed on the device or shipped with it. Without that the
bearer crosses an open access point in the clear, which would undo the care taken minting it.
