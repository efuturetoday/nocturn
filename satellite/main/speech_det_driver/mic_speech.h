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
    MIC_EVT_SPEECH_END, // silence held long enough to call the utterance over; PCM stops
} mic_speech_event_t;

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

#ifdef __cplusplus
}
#endif
