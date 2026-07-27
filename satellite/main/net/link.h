#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// link is the standing connection to nocturn.
//
// It stays open for as long as the device is switched on, and carries everything: the commands that
// open and close a spoken session, the audio in both directions, and — because the daemon has a
// target only while something is connected — anything it wants to say on its own.
//
// The Gemini session is what costs money per minute, and that one opens on the wake word. This
// socket costs nothing to hold, and holding it is what removes a second of setup from the front of
// every question, and what makes a spoken reminder possible at all.

// link_control_cb receives one tagged JSON message from the daemon, NUL-terminated.
typedef void (*link_control_cb)(const char *json, void *user);

// link_audio_cb receives one chunk of speech to play: 16 kHz mono PCM16, already converted by the
// daemon, so the board never learns another rate exists.
//
// Both callbacks run on the WebSocket client's own task. Neither may block: this task also drives
// the connection's keepalive, and stalling it drops the link.
typedef void (*link_audio_cb)(const uint8_t *pcm, size_t bytes, void *user);

// link_start opens the connection and keeps it open, reconnecting on its own. It returns once the
// attempt has begun, not once it has succeeded.
esp_err_t link_start(const char *host, uint16_t port, const char *path, const char *bearer,
                     link_control_cb on_control, link_audio_cb on_audio, void *user);

// link_connected reports whether the socket is up right now.
bool link_connected(void);

// link_send_text queues one tagged command. Returns false when it could not be queued — the link is
// down, or the queue is full because the socket has stopped draining. A caller whose message carries
// state rather than news must treat false as "not sent" and keep the state.
//
// Both senders queue rather than send: the socket has exactly one writing task, because the client
// serialises sends on a lock with a timeout and answers a lost race with a write error, which it
// then treats as reason to drop the connection.
bool link_send_text(const char *json);

// link_send_audio queues one chunk of microphone PCM. A dropped frame is not worth retrying: by the
// time the link is back the audio is stale, and the far side would hear a jump rather than a pause.
bool link_send_audio(const uint8_t *pcm, size_t bytes);

#ifdef __cplusplus
}
#endif
