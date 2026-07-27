#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// mic_speech is the microphone side: the audio front end, the wake word, and voice activity.
//
// It deliberately does NOT recognise what was said. The vendor demo ran a command model on the
// device and reported command ids; here the words leave the board and a language model does the
// understanding. A fixed command set is precisely the thing a satellite exists to replace.
//
// What leaves this module is cleaned 16 kHz mono PCM16 — echo-cancelled against the speaker,
// noise-suppressed, and gated by the wake word — which is exactly the format a live session wants.

// The edges of an utterance are reported as events on the default loop — SAT_EV_WAKE,
// SAT_EV_VOICE, SAT_EV_UTTERANCE_END, and SAT_EV_MIC_DEAD when this module gives up. See state.h.
//
// Posted rather than called back, because this runs on the detect loop: whatever a consumer does
// would otherwise run on the task that must keep fetching, and a stall there costs the front end the
// alignment its echo cancellation depends on.
//
// SAT_EV_VOICE in particular is the RAW leading edge of voice activity, reported whether or not a
// session is open — the moment it matters most is while the assistant is talking, which is exactly
// when the old code was not looking. It is not an utterance boundary and must not be treated as one:
// it fires on any voice the front end hears, including a cough and including the board's own
// speaker. What it means is the state machine's to decide.

// mic_pcm_sink_t receives one chunk of cleaned mono PCM16 while a session is open.
//
// It runs ON the fetch loop, so it must not block. Hand the samples to a queue and return: anything
// slower starves the front end, and a starved front end loses the alignment its echo canceller
// needs between what is playing and what the microphone hears.
typedef void (*mic_pcm_sink_t)(const int16_t *pcm, size_t samples, void *user);

// mic_speech_start brings up the front end and begins listening for the wake word. sink may be NULL.
esp_err_t mic_speech_start(mic_pcm_sink_t sink, void *user);

// mic_speech_voice reports the detector's current answer: is a voice present right now. The level to
// MIC_EVT_VOICE's edge, for anything that needs to ask rather than be told.
bool mic_speech_voice(void);

#ifdef __cplusplus
}
#endif
