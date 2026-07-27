#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// audio_out is the speaker side: a queue in front of the codec, drained by its own task.
//
// The queue is the point. Writing to the codec blocks until the hardware has taken the samples, and
// the caller here is the audio front end's fetch loop — the one loop that must never stall. If it
// misses a fetch the front end starves, samples are dropped, and the echo canceller loses the
// alignment between what is playing and what the microphone hears. A satellite whose canceller has
// drifted hears itself and talks over the person.

// audio_out_init starts the drain task. Call once, after the board is up.
esp_err_t audio_out_init(void);

// audio_out_write queues one chunk of MONO 16 kHz PCM16 for playback and returns immediately.
//
// A full queue drops the chunk rather than waiting: dropping audio costs a click, and blocking here
// costs the echo canceller. Returns ESP_ERR_NO_MEM on a drop, which callers may ignore but should
// count.
esp_err_t audio_out_write(const int16_t *mono, size_t samples);

// audio_out_flush discards whatever is queued but not yet played — for a barge-in, where everything
// still in the queue answers a question the person has already abandoned.
void audio_out_flush(void);

// audio_out_amp raises or drops the speaker amplifier.
//
// Dropping it between utterances is not power saving, it is silence: with nothing queued the codec
// keeps repeating whatever was last in its DMA buffer, which is audible as a held tone. Flush with
// audio_out_silence first, then drop the amplifier.
void audio_out_amp(bool on);

// audio_out_silence queues ms of zeros, so the codec's last buffer is overwritten with nothing
// rather than left ringing.
void audio_out_silence(int ms);

// audio_out_stats reports and resets what the drain task has done since the last call: how many
// chunks and samples reached the codec, and the last error it returned. Silence has more than one
// cause, and only separate counters tell them apart.
void audio_out_stats(uint32_t *chunks, uint32_t *samples, int *err);

#ifdef __cplusplus
}
#endif
