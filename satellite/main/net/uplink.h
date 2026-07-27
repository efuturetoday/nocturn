#pragma once

#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// uplink is the microphone side of the link: a queue in front of the socket, drained by its own task.
//
// It exists for the same reason audio_out does, mirrored. The samples arrive on the audio front
// end's fetch loop, which must never wait — a missed fetch starves the front end, and a starved
// front end loses the alignment its echo canceller needs between what is playing and what the
// microphone hears. Sending on a socket can block for as long as the network feels like, so the two
// cannot touch.

// uplink_start begins draining. Call once, after the link exists.
esp_err_t uplink_start(void);

// uplink_write queues one chunk of mono 16 kHz PCM16 and returns immediately.
//
// A full queue drops the chunk rather than waiting: a dropped frame costs a click at the far end,
// while a blocked fetch loop costs the echo cancellation this whole device depends on. Returns
// ESP_ERR_NO_MEM on a drop, which the caller should count.
esp_err_t uplink_write(const int16_t *pcm, size_t samples);

// uplink_open and uplink_close gate the queue: audio only leaves while a conversation is open, so a
// board that is merely switched on is not streaming a room to a daemon.
void uplink_open(void);
void uplink_close(void);

#ifdef __cplusplus
}
#endif
