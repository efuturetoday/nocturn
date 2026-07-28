#include "state.h"

#include <string.h>

#include "freertos/FreeRTOS.h"

#include "esp_event.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_wifi.h"

#include "rgb_led_driver.h"

static const char *TAG = "sat/state";

ESP_EVENT_DEFINE_BASE(SAT_EVENT);

// How long after playback starts that voice activity is discarded.
//
// Raising the speaker amplifier clicks, and the microphone hears the click as a voice. It is
// invisible to echo cancellation — the canceller subtracts the playback signal it is handed
// DIGITALLY, and the click happens after the DAC, inside the amplifier, where digitally there is
// silence. Nothing downstream can reach it, and measurement agreed: exactly one spurious detection
// per playback, always at the start, the same for a three second replay as for a five second one.
//
// So it belongs here. This module raised the amplifier, so this module discards what the amplifier
// caused — a self-inflicted observation is not evidence. The cost is nothing real: the assistant has
// barely started its first word, and nobody interrupts a sentence that has not begun.
#define SPEAKING_DEAF_US 200000

// What this module believes. Written only by the event handler, which runs on one task, so no lock:
// the alternative would be a mutex protecting data that has exactly one writer.
static struct {
    bool provisioned;
    bool has_address;
    bool has_link;
    bool link_rejected;
    bool mic_alive;
    bool session; // an utterance is being streamed
    bool playing;  // the speaker is running
    sat_state_t remote; // what the daemon last said the conversation was doing
    bool remote_known;
    int64_t speaking_since; // for SPEAKING_DEAF_US; 0 when not speaking
} f = {.mic_alive = true, .provisioned = true};

// When a voice was last heard over a reply. The daemon's interrupt is timed against it, which is the
// only way to compare deciding here with deciding upstream.
static int64_t heard_at;

static sat_state_t current = SAT_BOOT;
static bool started; // BOOT ends at the first fact of any kind

// resolve derives the state rather than storing it.
//
// A stored state has to be transitioned correctly from everywhere, and one missed transition strands
// the ring on a lie until something else happens to move it. Derived, there is nothing to get out of
// step: the facts are whatever they are and the answer follows.
//
// The order is total and fixed. Reachability outranks conversation because it is the truth — a
// device with no socket cannot be listening, whatever it was doing a moment ago, and showing green
// then would be a lie. APPROVAL outranks the conversation because an approval nobody notices stalls
// everything behind it, and because the person's attention has to move to another device, which is
// exactly when this ring must stop describing this one.
static sat_state_t resolve(void)
{
    if (!f.provisioned || f.link_rejected || !f.mic_alive) {
        return SAT_FAULT;
    }
    if (!f.has_address) {
        return started ? SAT_NO_NETWORK : SAT_BOOT;
    }
    if (!f.has_link) {
        return SAT_NO_DAEMON;
    }
    if (f.remote_known && f.remote == SAT_APPROVAL) {
        return SAT_APPROVAL;
    }
    // SPEAKING is a LOCAL fact — the speaker is running — and not the daemon's report that the model
    // is talking. The two are nearly the same thing and the difference is load-bearing.
    //
    // Entering this state starts the window in which voice activity is discarded, and that window
    // exists to cover the amplifier's switch-on click. The click is a hardware event on this board,
    // at a moment this board chooses. Hanging its suppression on a message from the daemon would put
    // the daemon's latency in charge of a window covering something the daemon never sees, and the
    // click would land outside it exactly when the network is slow.
    //
    // It also outranks the session for the same reason it is local: while the speaker runs, the ring
    // must not show green, whatever anyone else believes.
    if (f.playing) {
        return SAT_SPEAKING;
    }
    // The daemon knows things this board cannot: that the model is composing, that a tool is running,
    // that speech is on its way but has not arrived. But only while a session is open — outside one
    // its last word is stale, and a device sitting on "thinking" after the conversation ended is the
    // same failure as one sitting on green.
    if (f.session) {
        if (f.remote_known && f.remote == SAT_THINKING) {
            return SAT_THINKING;
        }
        return SAT_LISTENING;
    }
    return SAT_IDLE;
}

// paint maps a state onto the ring.
//
// Tempo carries as much meaning as colour here. A slow breath is patience — something is wrong and
// the device is waiting it out. A normal breath is an invitation — it is waiting on YOU. Across a
// room the tempo is the only thing separating them.
//
// Green appears exactly once, on LISTENING. The single question a person asks this ring is "may I
// talk now", and one colour should answer it.
static void paint(sat_state_t s)
{
    switch (s) {
    case SAT_BOOT:
        rgb_show(RGB_BREATHE_SLOW, RGB_WHITE);
        return;
    case SAT_NO_NETWORK:
        rgb_show(RGB_BREATHE_SLOW, RGB_AMBER);
        return;
    case SAT_NO_DAEMON:
        // Spinning, because discovery genuinely is running. Amber against THINKING's blue: the shape
        // means "working on it" in both cases, and learning two shapes beats learning nine.
        rgb_show(RGB_SPIN, RGB_AMBER);
        return;
    case SAT_FAULT:
        // The slow blink: broken, but nothing is waiting on an answer.
        rgb_show(RGB_BLINK_SLOW, RGB_RED);
        return;
    case SAT_IDLE:
        // Dim on purpose. This is the resting state and the device sits in a room people sleep in.
        rgb_show(RGB_SOLID, RGB_DIM_BLUE);
        return;
    case SAT_LISTENING:
        rgb_show(RGB_BREATHE, RGB_GREEN);
        return;
    case SAT_THINKING:
        // The four drifting colours, pulled tight and circling. RGB_OFF because the pattern carries
        // its own palette — see rgb_led_driver.h.
        rgb_show(RGB_SIRI_THINK, RGB_OFF);
        return;
    case SAT_SPEAKING:
        // The same colours, loose and swelling with the actual speech: the ring is driven by the
        // samples leaving the speaker, so it moves with the words rather than beside them.
        //
        // Not green, and now not a single hue at all: the person should not feel invited to talk
        // while the assistant does, and nothing on this ring says "this is the assistant's turn"
        // more plainly than the one pattern no other state uses.
        rgb_show(RGB_SIRI, RGB_OFF);
        return;
    case SAT_APPROVAL:
        // The only magenta and the only quick blink. It means: go look at your phone.
        rgb_show(RGB_BLINK, RGB_MAGENTA);
        return;
    }
}

// settle recomputes, and repaints only on a change.
static void settle(void)
{
    sat_state_t next = resolve();
    if (next == current) {
        return;
    }
    // Entering SPEAKING starts the deaf window; leaving it clears the clock rather than leaving a
    // stale timestamp that would suppress the first barge-in of the NEXT reply.
    f.speaking_since = next == SAT_SPEAKING ? esp_timer_get_time() : 0;

    ESP_LOGI(TAG, "%s → %s", state_name(current), state_name(next));
    current = next;
    paint(next);
}

// remote_state maps what the daemon says onto a state. Unknown strings are ignored rather than
// guessed at: a daemon that grows a state this firmware has not heard of should leave the ring
// showing what it last knew, not blank it.
static bool remote_state(const char *name, sat_state_t *out)
{
    if (strcmp(name, "listening") == 0) { *out = SAT_LISTENING; return true; }
    if (strcmp(name, "thinking") == 0)  { *out = SAT_THINKING;  return true; }
    if (strcmp(name, "speaking") == 0)  { *out = SAT_SPEAKING;  return true; }
    if (strcmp(name, "approval") == 0)  { *out = SAT_APPROVAL;  return true; }
    if (strcmp(name, "idle") == 0)      { *out = SAT_IDLE;      return true; }
    ESP_LOGW(TAG, "daemon reported an unknown state: %s", name);
    return false;
}

// on_voice decides what a detected voice MEANS, which is the whole reason the raw signal is routed
// here instead of acted on where it is produced.
static void on_voice(void)
{
    if (current != SAT_SPEAKING) {
        // Not a barge-in, but still worth saying: the daemon hangs up on silence, and only this
        // board can tell it the room is not silent. Its own microphone stream would do it eventually
        // — the model transcribes what it understood — but that arrives later and only for speech
        // that made sense, which is the wrong test for whether anyone is still here.
        if (f.session) {
            esp_event_post(SAT_EVENT, SAT_EV_SPEECH, NULL, 0, 0);
        }
        return;
    }
    if (esp_timer_get_time() - f.speaking_since < SPEAKING_DEAF_US) {
        ESP_LOGD(TAG, "voice during the amplifier's own click — ignored");
        return;
    }
    // A person talking over the reply. Whether that stops anything is BARGE_IN_LOCAL's answer; the
    // moment is recorded either way, because the whole question is how long the other route takes.
    heard_at = esp_timer_get_time();
#if BARGE_IN_LOCAL
    // What this module can do is decide it happened; silencing the speaker and telling the daemon
    // belong to whoever owns those, so it says so and they act.
    ESP_LOGI(TAG, "barge-in (local)");
    rgb_flash(RGB_WHITE);
    esp_event_post(SAT_EVENT, SAT_EV_BARGE_IN, NULL, 0, 0);
#else
    ESP_LOGI(TAG, "voice over the reply — waiting for the daemon to say so");
    return; // the ring keeps showing SPEAKING; the model has the microphone and will notice
#endif
    f.remote_known = false; // the daemon's "speaking" is now wrong; it will say so shortly
    settle();
}

static void on_sat(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    switch (id) {
    case SAT_EV_WAKE:
        started = true;
        f.session = true;
        f.remote_known = false;
        break;
    case SAT_EV_VOICE:
        on_voice();
        return; // on_voice settles for itself, or deliberately does not
    case SAT_EV_UTTERANCE_END:
        f.session = false;
        break;
    case SAT_EV_PLAYBACK_START:
        f.playing = true;
        break;
    case SAT_EV_PLAYBACK_END:
        f.playing = false;
        break;
    case SAT_EV_MIC_DEAD:
        // The worst failure this device has: deaf, while the link stays up and the heartbeat keeps
        // printing "alive". Nothing else notices, so the dying task says so on its way out.
        ESP_LOGE(TAG, "front end stopped — the board is deaf");
        f.mic_alive = false;
        break;
    case SAT_EV_LINK_UP:
        started = true;
        if (!f.has_link) {
            rgb_flash(RGB_GREEN); // connecting is a moment, not a state
        }
        f.has_link = true;
        break;
    case SAT_EV_LINK_DOWN:
        f.has_link = false;
        f.remote_known = false;
        break;
    case SAT_EV_LINK_REJECTED:
        ESP_LOGE(TAG, "the daemon does not accept this device's token — re-enrol it");
        f.link_rejected = true;
        break;
    case SAT_EV_UNPROVISIONED:
        f.provisioned = false;
        break;
    case SAT_EV_REMOTE_STATE:
        f.remote_known = remote_state((const char *)data, &f.remote);
        break;
    default:
        return;
    }
    settle();
}

static void on_net(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        started = true;
        f.has_address = true;
    } else if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        started = true;
        f.has_address = false;
        // Losing the network takes the socket with it, and the WebSocket client will say so in its
        // own time. Saying it here as well means the ring never shows NO_DAEMON for a board that has
        // no network at all — which would send someone to check the wrong machine.
        f.has_link = false;
        f.remote_known = false;
    } else {
        return;
    }
    settle();
}

// state_start registers and paints. It creates NOTHING.
//
// The event loop and the ring are both shared, and this module is merely their first user — being
// first is not ownership. Creating them here is how the loop came to be created twice, once here and
// once in wifi_start, which was a boot loop whose symptom pointed at neither.
//
// Both belong to app_main, which is the only place that can order them, and the ordering is real:
// the loop before any handler, the ring before anything paints, this module before any source posts.
esp_err_t state_start(void)
{
    ESP_ERROR_CHECK(esp_event_handler_instance_register(SAT_EVENT, ESP_EVENT_ANY_ID, on_sat, NULL, NULL));
    ESP_ERROR_CHECK(esp_event_handler_instance_register(WIFI_EVENT, WIFI_EVENT_STA_DISCONNECTED, on_net, NULL, NULL));
    ESP_ERROR_CHECK(esp_event_handler_instance_register(IP_EVENT, IP_EVENT_STA_GOT_IP, on_net, NULL, NULL));

    paint(current);
    return ESP_OK;
}

sat_state_t state_get(void) { return current; }

void state_report_remote_interrupt(void)
{
    if (heard_at == 0) {
        ESP_LOGI(TAG, "daemon interrupt, with no voice heard here — the model decided on its own");
        return;
    }
    ESP_LOGI(TAG, "daemon interrupt %lld ms after this board heard the voice",
             (esp_timer_get_time() - heard_at) / 1000);
    heard_at = 0;
}

const char *state_name(sat_state_t state)
{
    switch (state) {
    case SAT_BOOT: return "boot";
    case SAT_NO_NETWORK: return "no-network";
    case SAT_NO_DAEMON: return "no-daemon";
    case SAT_FAULT: return "fault";
    case SAT_IDLE: return "idle";
    case SAT_LISTENING: return "listening";
    case SAT_THINKING: return "thinking";
    case SAT_SPEAKING: return "speaking";
    case SAT_APPROVAL: return "approval";
    }
    return "?";
}
