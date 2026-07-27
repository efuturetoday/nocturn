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

// mic_speech_event_t marks the edges of an utterance.
typedef enum {
    MIC_EVT_AWAKE,      // the wake word fired; PCM starts flowing
    MIC_EVT_VOICE,      // voice activity began — see below; fires with no session open
    MIC_EVT_SPEECH_END, // silence held long enough to call the utterance over; PCM stops
} mic_speech_event_t;

// MIC_EVT_VOICE is the raw leading edge of voice activity, reported whether or not a session is
// open. It is the local half of a barge-in: while the assistant is talking, this is the board
// noticing that a person started talking over it, and it costs one detector frame instead of a round
// trip to the daemon and back.
//
// It is NOT an utterance boundary and must not be treated as one. It fires on any voice the front end
// hears, including a cough and including — if echo cancellation is not holding — the board's own
// speaker. Whoever consumes it decides what it means.

// mic_pcm_sink_t receives one chunk of cleaned mono PCM16 while a session is open.
//
// It runs ON the fetch loop, so it must not block. Hand the samples to a queue and return: anything
// slower starves the front end, and a starved front end loses the alignment its echo canceller
// needs between what is playing and what the microphone hears.
typedef void (*mic_pcm_sink_t)(const int16_t *pcm, size_t samples, void *user);

// mic_speech_event_cb_t is called on the same loop, under the same rule.
typedef void (*mic_speech_event_cb_t)(mic_speech_event_t event, void *user);

// mic_speech_start brings up the front end and begins listening for the wake word. Either callback
// may be NULL.
esp_err_t mic_speech_start(mic_pcm_sink_t sink, mic_speech_event_cb_t on_event, void *user);

// mic_speech_session_open reports whether an utterance is currently being streamed.
bool mic_speech_session_open(void);

// mic_speech_voice reports the detector's current answer: is a voice present right now. The level to
// MIC_EVT_VOICE's edge, for anything that needs to ask rather than be told.
bool mic_speech_voice(void);

#ifdef __cplusplus
}
#endif
