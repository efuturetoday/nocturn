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
// Dropping it between utterances is not power saving, it is silence: an amplifier held up hisses
// into a quiet room. Flush with audio_out_silence first, then drop it.
//
// Raising it clicks, and the microphone hears it as a voice — see the definition for why nothing in
// the front end can remove that. The caller is expected to ignore voice activity briefly afterwards,
// since it is the only party that knows the click was its own doing.
void audio_out_amp(bool on);

// audio_out_silence queues ms of zeros, so the codec's last buffer is overwritten with nothing
// rather than left ringing.
void audio_out_silence(int ms);

// audio_out_stats reports and resets what the drain task has done since the last call: how many
// chunks and samples reached the codec, and the last error it returned. Silence has more than one
// cause, and only separate counters tell them apart.
void audio_out_stats(uint32_t *chunks, uint32_t *samples, int *err);

// audio_out_capacity is the size of the queue, in bytes. The initial credit.
size_t audio_out_capacity(void);

// audio_out_take_freed reports how many bytes have been played since the last call, and forgets
// them. That number IS the credit to hand back to the sender.
//
// Freed, not depth: what the sender needs to know is how much more it may send, and only what has
// actually left the queue answers that. Depth would count bytes still waiting to be played, which is
// room the queue does not have.
size_t audio_out_take_freed(void);

// audio_out_depth is what is queued but unplayed, in bytes. Instrumentation only: a credit window is
// sized against how close the queue came to empty, and a maximum tells you nothing about that.
size_t audio_out_depth(void);

#ifdef __cplusplus
}
#endif
