#pragma once

#include <stdbool.h>

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
    SAT_CAPTURE,    // enrolment: the microphone streams to the daemon and nothing answers
} sat_state_t;

// Everything that changes the state arrives as an event on THIS MODULE'S OWN loop — see state_post.
//
// Nothing is polled, and that is not a style preference: a socket that drops and reconnects between
// two samples is invisible to a poll, and leaves the ring showing a link that briefly did not exist.
// For a device whose whole job is to be honest about its own state, "we sampled and it looked fine"
// is the wrong default.
//
// Posting also keeps this module off both hot tasks. The front end's detect loop must keep fetching
// or echo cancellation loses its alignment; the socket's callbacks run on the WebSocket task, where
// blocking drops the connection. A post is bounded and does not block, so both callers stay as
// trivial as they are required to be.
//
// WiFi and IP arrive as SAT_EV_NET_UP and SAT_EV_NET_DOWN. They are system events, which this module
// subscribes to on the SYSTEM loop and does nothing with but forward here — so that one machine
// reads one queue, and a reconnect scan cannot hold up a transition.
ESP_EVENT_DECLARE_BASE(SAT_EVENT);

typedef enum {
    SAT_EV_NET_UP,         // the board has an address
    SAT_EV_NET_DOWN,       // it does not
    SAT_EV_WAKE,           // the wake word fired
    SAT_EV_VOICE,          // voice activity began — raw, means nothing on its own. See state.c
    SAT_EV_PLAYBACK_START, // the speaker is about to run. Post BEFORE raising the amplifier
    SAT_EV_PLAYBACK_END,   // it has stopped
    SAT_EV_MIC_DEAD,       // the front end's detect loop exited; the board is deaf from here on
    SAT_EV_LINK_UP,        // the socket to the daemon is open
    SAT_EV_LINK_DOWN,      // it is not
    SAT_EV_LINK_REJECTED,  // the daemon does not accept this device's token; retrying cannot fix it
    SAT_EV_UNPROVISIONED,  // no credentials in NVS
    SAT_EV_REMOTE_STATE,   // the daemon's view of the conversation; data is a NUL-terminated string
    SAT_EV_BARGE_IN,       // a person talked over the reply; posted BY this module, see state.c
    SAT_EV_SPEECH,         // a voice was heard with no reply playing; posted BY this module
    SAT_EV_BUTTON_DOWN,    // a button went down; data is a button_id_t
    SAT_EV_BUTTON_UP,      // and back up
    SAT_EV_TICK,           // 500 ms clock, so the machine can notice what did NOT arrive
    SAT_EV_CAPTURE_START,  // the daemon asks this board to record its microphone for enrolment
    SAT_EV_CAPTURE_STOP,   // and to stop. The board also stops itself — see CAPTURE_MAX_US
} sat_event_id_t;

// BARGE_IN_LOCAL decides who stops the speaker when a person talks over it.
//
// On: this board does it, on its own voice detector, and tells the daemon afterwards.
// Off: nothing local happens and the model works it out for itself — it is receiving the microphone
// either way — and the daemon sends voice.interrupt when it does.
//
// A flag rather than a decision, because the answer is a measurement nobody has yet: local detection
// costs 160-220 ms (the detector cannot fire on the first frame, and waits for vad_min_speech_ms of
// held speech), and going via the model costs that plus a round trip plus however long the model
// takes to notice. If the difference turns out to be small, everything upstream is less machinery
// for the same behaviour.
//
// Either way the board LOGS both moments, so the comparison is a number rather than an impression.
//
// OFF until the echo canceller is measured. With it on the board interrupts itself: the speaker
// starts, the microphone hears the residual echo, the detector calls it a voice, and the reply is
// flushed before a word of it is audible.
#define BARGE_IN_LOCAL 0

// state_post hands one event to the state machine's own task. Never blocks; a full queue is logged
// and dropped, because every caller is on a deadline of its own.
//
// Use this rather than esp_event_post: the machine runs on its OWN loop, not the system's, so that a
// transition calling into a driver cannot hold up WiFi and a reconnect scan cannot hold up a
// transition.
void state_post(sat_event_id_t ev, const void *data, size_t len);

// state_subscribe adds a second handler on the state machine's loop, for a consumer that must act on
// the same events — sending the protocol, driving the bench recording.
//
// It runs on the machine's task, serialised with the machine's own handler and with every other
// subscriber, which is the property worth having: no consumer of these events can see a half-applied
// transition, and none needs a lock.
esp_err_t state_subscribe(esp_event_handler_t handler);

// state_start creates this module's event loop and its tick timer, registers the handlers, and
// paints the initial state.
//
// What it does NOT create is the default event loop or the LED renderer: the caller must have both
// already, because this module is merely the first user of each and being first is not owning. Call
// once, BEFORE anything begins posting.
esp_err_t state_start(void);

// state_get is the current state, for the heartbeat line. Nothing should branch on it: whoever needs
// to act on a CHANGE should be reacting to the event that caused it.
sat_state_t state_get(void);

// state_conversation_active answers one question at one moment: may I have the speaker. The bench
// tool is the only caller, asking once per button press — no event expresses that question.
bool state_conversation_active(void);

// state_report_remote_interrupt notes that the daemon's interrupt arrived, and says how long after
// this board heard the voice itself. That gap IS the cost of deciding upstream, and it is the number
// BARGE_IN_LOCAL exists to be chosen against.
void state_report_remote_interrupt(void);

// state_name is the short lowercase name, for logs.
const char *state_name(sat_state_t state);

#ifdef __cplusplus
}
#endif
