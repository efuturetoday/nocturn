#pragma once

#include "esp_err.h"
#include "esp_event.h"

#ifdef __cplusplus
extern "C" {
#endif

// state is what the satellite believes about itself, and the ring is how it says so.
//
// A screenless device has one output. Every module that knows something — the radio, the socket, the
// front end, the daemon — reports it here, and this is the only place that decides what the seven
// pixels mean. Before this existed, four call sites painted colours with no notion of a state, and
// the ring lied: an unprovisioned board and a board merely waiting for the network were both solid
// red and indistinguishable.
//
// THE BOARD OWNS THE STATE. The daemon supplies facts. Everything a person can perceive as slow —
// the ring lighting up when they speak, an interruption registering — is decided from what this
// board itself observed, because a round trip is 150–250 ms on a LAN and immediacy ends around 200.
// When the daemon's belief contradicts what was just observed here, the observation wins: the person
// is in the room and the daemon is not.

typedef enum {
    SAT_BOOT,       // powering up, nothing established yet
    SAT_NO_NETWORK, // not associated, or associated without an address
    SAT_NO_DAEMON,  // on the network, but no socket — daemon down, or discovery still looking
    SAT_FAULT,      // will not recover without a person
    SAT_IDLE,       // link up, waiting for the wake word
    SAT_LISTENING,  // a session is open and the person may talk
    SAT_THINKING,   // the person stopped, nothing has come back yet
    SAT_SPEAKING,   // the speaker is running — a local fact, see state.c
    SAT_APPROVAL,   // a tool is waiting for a human decision on another device
} sat_state_t;

// Everything that changes the state arrives as an event on the default loop.
//
// Nothing is polled, and that is not a style preference: a socket that drops and reconnects between
// two samples is invisible to a poll, and leaves the ring showing a link that briefly did not exist.
// For a device whose whole job is to be honest about its own state, "we sampled and it looked fine"
// is the wrong default.
//
// Posting also keeps this module off both hot tasks. The front end's callbacks run on its fetch
// loop, where blocking starves echo cancellation; the socket's run on the WebSocket task, where
// blocking drops the connection. A post is bounded and does not block, so both callers stay as
// trivial as they are required to be.
//
// WiFi and IP are not in this list: those are system events this module subscribes to directly.
ESP_EVENT_DECLARE_BASE(SAT_EVENT);

typedef enum {
    SAT_EV_WAKE,           // the wake word fired
    SAT_EV_VOICE,          // voice activity began — raw, means nothing on its own. See state.c
    SAT_EV_UTTERANCE_END,  // silence held long enough to call the utterance over
    SAT_EV_PLAYBACK_START, // the speaker is about to run. Post BEFORE raising the amplifier
    SAT_EV_PLAYBACK_END,   // it has stopped
    SAT_EV_MIC_DEAD,       // the front end's detect loop exited; the board is deaf from here on
    SAT_EV_LINK_UP,        // the socket to the daemon is open
    SAT_EV_LINK_DOWN,      // it is not
    SAT_EV_LINK_REJECTED,  // the daemon does not accept this device's token; retrying cannot fix it
    SAT_EV_UNPROVISIONED,  // no credentials in NVS
    SAT_EV_REMOTE_STATE,   // the daemon's view of the conversation; data is a NUL-terminated string
    SAT_EV_BARGE_IN,       // a person talked over the reply; posted BY this module, see state.c
    SAT_EV_BUTTON_DOWN,    // a button went down; data is a button_id_t
    SAT_EV_BUTTON_UP,      // and back up
} sat_event_id_t;

// state_start registers the handlers and paints the initial state.
//
// It creates nothing. The caller must already have created the default event loop and started the
// LED renderer — this module is the first user of both, and being first is not owning. Call once,
// BEFORE anything begins posting.
esp_err_t state_start(void);

// state_get is the current state, for the heartbeat line. Nothing should branch on it: whoever needs
// to act on a change should be reacting to the event that caused it.
sat_state_t state_get(void);

// state_name is the short lowercase name, for logs.
const char *state_name(sat_state_t state);

#ifdef __cplusplus
}
#endif
