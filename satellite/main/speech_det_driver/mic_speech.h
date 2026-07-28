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
// The cleaned audio it produces — 16 kHz mono PCM16, echo-cancelled and noise-suppressed — goes into
// micbuf on every frame. Nothing leaves through this interface; whoever wants the microphone reads
// it there.

// THIS MODULE IS A DETECTOR. Two edges out, one command in, and no idea of a conversation at all —
// every question of the form "and what does that mean" belongs to state/.
//
// Posted rather than called back: this runs on the detect loop, and a consumer's work on that task
// costs the front end the alignment its echo cancellation depends on.
//
// SAT_EV_VOICE is the RAW leading edge of voice activity, reported whether or not anyone is
// listening. It is not an utterance boundary: it fires on any voice the front end hears, including a
// cough and including the board's own speaker.

// mic_speech_start brings up the front end and begins listening for the wake word. micbuf_init must
// have run first — this module starts writing to it immediately.
esp_err_t mic_speech_start(void);

// mic_speech_voice reports the detector's current answer: is a voice present right now. The level to
// SAT_EV_VOICE's edge, for anything that needs to ask rather than be told.
bool mic_speech_voice(void);

// mic_stats_t is what the detect loop has done since it was last asked. A deaf board has several
// causes that look identical from outside, and only separate counters tell them apart.
//
// Read fetches first: at zero the loop is not turning and nothing else here describes the present.
typedef struct {
    uint32_t fetches; // frames the front end returned
    uint32_t speech;  // of those, how many it called speech
    uint32_t wakes;   // verified wake words
    uint32_t samples; // cleaned samples produced; against fetches, the frame size actually delivered
    float volume_db;  // loudest input measured, before gain
    int32_t ref_peak; // loudest raw echo-reference slot, before the front end
    int32_t raw_peak; // loudest raw microphone slot, before the front end
    int armed;        // wakenet as enable_wakenet reported it: -1 fail, 0 off, 1 on
} mic_stats_t;

// mic_speech_stats reports and resets the counters. armed and volume_db are levels and survive.
void mic_speech_stats(mic_stats_t *out);

// mic_arm decides whether the wake word is listening.
//
// Off while a conversation runs, so that saying it mid-sentence does not restart one — and back on
// when the conversation ends. Both are the caller's to time; this module has no notion of either
// moment, which is exactly why it can no longer get the pairing wrong.
//
// It is the ONLY command this module takes. There is deliberately no "start capturing": the audio
// never stops flowing into micbuf, so wanting it is a reader's decision and not a switch anybody
// else can flip. A single global switch is what let a bench recording silence a live conversation.
void mic_arm(bool on);

#ifdef __cplusplus
}
#endif
