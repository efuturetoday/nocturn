#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// uplink is the microphone side of the link: one task reading micbuf and writing the socket.
//
// It takes audio from nobody, so it can block nobody. Samples are produced on the front end's fetch
// loop, which must never wait, and a socket write can block for as long as the network likes; the
// two never touch.

// uplink_start begins the sender. Call once, after micbuf and the link exist.
esp_err_t uplink_start(void);

// uplink_stats reports and resets what reached the socket since the last call, plus how often the
// sender fell far enough behind that micbuf lapped it.
void uplink_stats(uint32_t *bytes, uint32_t *fails, uint32_t *late);

// uplink_open and uplink_close decide whether audio leaves at all, so a board that is merely
// switched on is not streaming a room to a daemon. Opening also picks where in micbuf to start.
void uplink_open(void);
void uplink_close(void);

// uplink_gate replaces the microphone with silence while muted — half duplex.
//
// The model must never hear this board's own voice, and the echo canceller does not remove enough
// of it to guarantee that (its residue measures louder than a person). Silence rather than a pause,
// so the far side's voice detection sees a turn end instead of a stalled stream. The cost is
// barge-in: a person talking over the reply is inaudible upstream while the gate is closed.
void uplink_gate(bool muted);

#ifdef __cplusplus
}
#endif
