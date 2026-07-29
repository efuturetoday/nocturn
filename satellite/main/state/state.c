#include "state.h"

#include <stdatomic.h>
#include <string.h>

#include "freertos/FreeRTOS.h"

#include "esp_check.h"
#include "esp_event.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_wifi.h"

#include "audio_out.h"
#include "mic_speech.h"
#include "rgb_led_driver.h"
#include "uplink.h"

static const char *TAG = "sat/state";

ESP_EVENT_DEFINE_BASE(SAT_EVENT);

// (state, signal) -> state, with actions on entry and exit. transition() lists every way each state
// can be left.

// Its own task and queue, not the system loop: transitions call into drivers, and the system loop
// belongs to WiFi and lwIP.
static esp_event_loop_handle_t loop;
static esp_timer_handle_t ticker;
#define QUEUE_DEPTH 16
#define TASK_STACK (4 * 1024)
#define TASK_PRIORITY 3 // below the audio tasks; a late colour is invisible, a late fetch is not

// Voice activity is ignored this long after the AMPLIFIER is raised — not after playback starts,
// which is a different moment on every path but the bench replay. Raising it clicks, the microphone
// hears it, and echo cancellation cannot remove it: the click happens after the DAC, where the
// canceller's reference is silent. Measured at one false trigger per playback.
#define AMP_DEAF_US 200000

// A wake word left UNACKNOWLEDGED this long is abandoned. voice.wake can be lost, and a conversation
// has the wake word switched off, so without this the board can never start another one.
#define WAKE_ACK_US 5000000

// The machine's clock, so it can notice what did NOT arrive.
#define TICK_US 500000

// A recording stops itself after this long, whatever the daemon does or fails to do.
//
// An enrolment utterance is a minute of talking; two minutes is generous. The limit exists because
// the failure it prevents is invisible: a caller that crashes between start and stop leaves a
// microphone streaming a room, and nothing in the house would say so. Fail closed, as everywhere
// else a default decides how much authority something keeps.
#define CAPTURE_MAX_US 120000000

static sat_state_t state = SAT_BOOT;

// Guards, not states: conditions a transition tests.
static bool has_address;
static bool has_link;

static int64_t entered_at; // for WAKE_ACK_US — this task's alone

// Last voice over a reply, to time the daemon's interrupt against. Atomic because the daemon's side
// of that measurement arrives on the WebSocket task: 64 bits read in halves across a write is a
// number that was never true, and this one exists to be believed.
static _Atomic int64_t heard_at;

// Whether the daemon has confirmed the conversation this board asked for.
//
// voice.state carries no session identity, so an "idle" cannot be attributed to the session it came
// from. "listening" confirms THIS session; until it arrives, every "idle" belongs to a conversation
// already over. It also separates an unanswered wake word from a conversation in progress — see
// WAKE_ACK_US.
static bool acked;

static const char *ev_name(sat_event_id_t ev);

// Green is held for LISTENING alone: the ring's one question is "may I talk now". Tempo carries
// meaning too — a slow breath is patience, a normal breath is an invitation. The flash on LINK_UP is
// the one exception, and a flash is not a state.
static void paint(sat_state_t s)
{
    switch (s) {
    case SAT_BOOT:       rgb_show(RGB_BREATHE_SLOW, RGB_WHITE); return;
    case SAT_NO_NETWORK: rgb_show(RGB_BREATHE_SLOW, RGB_AMBER); return;
    case SAT_NO_DAEMON:  rgb_show(RGB_SPIN, RGB_AMBER);         return; // discovery is running
    case SAT_FAULT:      rgb_show(RGB_BLINK_SLOW, RGB_RED);     return;
    case SAT_IDLE:       rgb_show(RGB_SOLID, RGB_DIM_BLUE);     return; // dim: people sleep here
    case SAT_LISTENING:  rgb_show(RGB_BREATHE, RGB_GREEN);      return;
    case SAT_THINKING:   rgb_show(RGB_SPIN, RGB_BLUE);          return;
    case SAT_SPEAKING:   rgb_show(RGB_WAVE, RGB_CYAN);          return; // not green: do not talk
    case SAT_APPROVAL:   rgb_show(RGB_BLINK, RGB_MAGENTA);      return; // go look at your phone
    // Red, and deliberately so despite SAT_FAULT also being red: that one blinks slowly, this one is
    // steady, and the two are told apart by motion rather than by colour. The recording convention is
    // understood by everyone who walks into the room, which is the whole point of showing it.
    case SAT_CAPTURE:    rgb_show(RGB_SOLID, RGB_RED);           return; // being recorded
    }
}

// The four states of an exchange in progress. Microphone, uplink and amplifier are up for all of
// them, so entering and leaving the group are the only moments those change.
static bool in_conversation(sat_state_t s)
{
    return s == SAT_LISTENING || s == SAT_THINKING || s == SAT_SPEAKING || s == SAT_APPROVAL;
}

// The only place any driver is commanded, and mic_arm's two halves are paired here so they cannot be
// separated.
//
// Nothing starts or stops the microphone: the front end writes every frame into micbuf regardless,
// and uplink_open/close decide only whether any of it leaves the building. A conversation is a
// reason to send, not a reason to listen.
static void leave(sat_state_t s, sat_state_t to)
{
    // A recording is not a conversation, so it is paired here rather than folded into
    // in_conversation: it must not raise the amplifier, touch playback credit, or arm anything that
    // could answer. All it ever did was open the uplink.
    if (s == SAT_CAPTURE) {
        uplink_close();
        mic_arm(true);
    }
    if (in_conversation(s) && !in_conversation(to)) {
        uplink_close();
        audio_out_amp(false);
        audio_out_flush();
        audio_out_take_freed(); // as entering does; credit for a finished session belongs to nobody
        mic_arm(true);
    }
}

static void enter(sat_state_t s, sat_state_t from)
{
    entered_at = esp_timer_get_time();
    // Half duplex: the microphone goes silent upstream for exactly as long as the speaker runs.
    // See uplink_gate for why this is an invariant and not a tuning choice.
    if (s == SAT_SPEAKING) {
        uplink_gate(true);
    } else if (from == SAT_SPEAKING) {
        uplink_gate(false);
    }
    if (s == SAT_CAPTURE) {
        mic_arm(false);     // the wake word must not turn a recording into a conversation
        uplink_gate(false); // nothing plays here, so nothing has to be gated against
        uplink_open();
    }
    if (in_conversation(s) && !in_conversation(from)) {
        acked = false;  // nothing the daemon said before belongs to this conversation
        mic_arm(false); // saying it mid-sentence must not restart a conversation
        uplink_open();
        audio_out_flush();
        audio_out_take_freed();
        audio_out_amp(true);
    }
}

static void go(sat_state_t to)
{
    if (to == state) {
        return; // a signal that does not move the machine must not re-run its actions
    }
    ESP_LOGI(TAG, "%s → %s", state_name(state), state_name(to));
    sat_state_t from = state;
    leave(from, to);
    state = to;
    enter(to, from);
    paint(to);
}

// Signals that mean the same from every state. Each is a fact that outranks the conversation: a
// device with no socket cannot be listening, whatever it believed a moment ago.
static bool universal(sat_event_id_t ev)
{
    switch (ev) {
    case SAT_EV_UNPROVISIONED:
        go(SAT_FAULT);
        return true;
    case SAT_EV_LINK_REJECTED:
        ESP_LOGE(TAG, "the daemon does not accept this device's token — re-enrol it");
        go(SAT_FAULT);
        return true;
    case SAT_EV_MIC_DEAD:
        ESP_LOGE(TAG, "front end stopped — the board is deaf");
        go(SAT_FAULT);
        return true;
    case SAT_EV_NET_DOWN:
        has_address = false;
        has_link = false; // the socket cannot survive the network
        go(SAT_NO_NETWORK);
        return true;
    case SAT_EV_LINK_DOWN:
        has_link = false;
        go(has_address ? SAT_NO_DAEMON : SAT_NO_NETWORK);
        return true;
    case SAT_EV_NET_UP:
        has_address = true;
        if (state == SAT_BOOT || state == SAT_NO_NETWORK) {
            go(SAT_NO_DAEMON);
        }
        return true;
    case SAT_EV_LINK_UP:
        if (!has_link) {
            rgb_flash(RGB_GREEN); // connecting is a moment, not a state
        }
        has_link = true;
        if (!in_conversation(state)) {
            go(SAT_IDLE);
        }
        return true;
    default:
        return false;
    }
}

// What a detected voice MEANS — which is why the raw signal is routed here rather than acted on
// where it is produced.
static void on_voice(void)
{
    // Asked of the amplifier, not of the state: on the real path it is raised when a conversation
    // begins and stays up, so no state marks the click.
    if (audio_out_amp_age_us() < AMP_DEAF_US) {
        return;
    }
    if (state != SAT_SPEAKING) {
        if (in_conversation(state)) {
            state_post(SAT_EV_SPEECH, NULL, 0); // the daemon hangs up on silence; this is not silence
        }
        return;
    }
    atomic_store(&heard_at, esp_timer_get_time());
#if BARGE_IN_LOCAL
    ESP_LOGI(TAG, "barge-in");
    rgb_flash(RGB_WHITE);
    state_post(SAT_EV_BARGE_IN, NULL, 0);
    go(SAT_LISTENING);
#endif
}

// The states the daemon owns. SPEAKING is not among them: it is entered from PLAYBACK_START, because
// the window that discards the amplifier's click must open when the click happens.
static void remote(const char *name)
{
    if (!in_conversation(state)) {
        return; // stale: it describes a conversation this board has already left
    }
    if (strcmp(name, "idle") == 0) {
        if (!acked) {
            return; // the previous conversation's ending, arriving after this one began
        }
        go(SAT_IDLE);
    } else if (strcmp(name, "listening") == 0) {
        acked = true; // the daemon has the session this board asked for
        go(SAT_LISTENING);
    } else if (strcmp(name, "thinking") == 0) {
        go(SAT_THINKING);
    } else if (strcmp(name, "approval") == 0) {
        go(SAT_APPROVAL);
    } else if (strcmp(name, "speaking") != 0) {
        ESP_LOGW(TAG, "daemon reported an unknown state: %s", name);
    }
}

static void transition(sat_event_id_t ev, const void *data)
{
    switch (ev) {
    case SAT_EV_WAKE:
        if (!has_link) {
            ESP_LOGW(TAG, "wake word with no link — ignored");
            return; // it would silence the wake word and reach nobody
        }
        if (state == SAT_IDLE) {
            go(SAT_LISTENING);
        }
        return;
    case SAT_EV_VOICE:
        on_voice();
        return;
    case SAT_EV_PLAYBACK_START:
        if (in_conversation(state)) {
            go(SAT_SPEAKING);
        }
        return;
    case SAT_EV_PLAYBACK_END:
        if (state == SAT_SPEAKING) {
            go(SAT_LISTENING);
        }
        return;
    case SAT_EV_REMOTE_STATE:
        remote((const char *)data);
        return;
    case SAT_EV_CAPTURE_START:
        // Only from idle: a recording must never interrupt a conversation, and the daemon asking
        // during one is asking about a board state it has not caught up with.
        if (state == SAT_IDLE) {
            ESP_LOGI(TAG, "recording for enrolment");
            go(SAT_CAPTURE);
        }
        return;
    case SAT_EV_CAPTURE_STOP:
        if (state == SAT_CAPTURE) {
            go(SAT_IDLE);
        }
        return;
    case SAT_EV_TICK:
        // The one thing no signal can announce: an answer that never came.
        if (state == SAT_LISTENING && !acked && esp_timer_get_time() - entered_at > WAKE_ACK_US) {
            ESP_LOGW(TAG, "no answer to the wake word — giving up on it");
            go(SAT_IDLE);
        }
        // Nor a stop that was never sent.
        if (state == SAT_CAPTURE && esp_timer_get_time() - entered_at > CAPTURE_MAX_US) {
            ESP_LOGW(TAG, "recording ran past its limit — stopping");
            go(SAT_IDLE);
        }
        return;
    default:
        return;
    }
}

static void on_sat(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (state == SAT_FAULT) {
        return; // only a person recovers a fault, and that means a reset
    }
    if (!universal((sat_event_id_t)id)) {
        transition((sat_event_id_t)id, data);
    }
}

// Runs on the SYSTEM loop, so it does one thing: forward.
static void on_net(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        state_post(SAT_EV_NET_UP, NULL, 0);
    } else if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        state_post(SAT_EV_NET_DOWN, NULL, 0);
    }
}

// A machine driven only by signals cannot notice something that failed to arrive.
static void tick(void *arg) { state_post(SAT_EV_TICK, NULL, 0); }

esp_err_t state_start(void)
{
    esp_event_loop_args_t args = {
        .queue_size = QUEUE_DEPTH,
        .task_name = "state",
        .task_priority = TASK_PRIORITY,
        .task_stack_size = TASK_STACK,
        .task_core_id = 0, // core 1 carries the audio front end's fetch loop
    };
    ESP_RETURN_ON_ERROR(esp_event_loop_create(&args, &loop), TAG, "loop");
    ESP_RETURN_ON_ERROR(
        esp_event_handler_instance_register_with(loop, SAT_EVENT, ESP_EVENT_ANY_ID, on_sat, NULL, NULL),
        TAG, "handler");
    ESP_RETURN_ON_ERROR(
        esp_event_handler_instance_register(WIFI_EVENT, WIFI_EVENT_STA_DISCONNECTED, on_net, NULL, NULL),
        TAG, "wifi");
    ESP_RETURN_ON_ERROR(
        esp_event_handler_instance_register(IP_EVENT, IP_EVENT_STA_GOT_IP, on_net, NULL, NULL),
        TAG, "ip");

    // Static, not local: the handle is the only way to ever stop or free this timer.
    const esp_timer_create_args_t timer = {.callback = tick, .name = "state_tick"};
    ESP_RETURN_ON_ERROR(esp_timer_create(&timer, &ticker), TAG, "timer");
    ESP_RETURN_ON_ERROR(esp_timer_start_periodic(ticker, TICK_US), TAG, "timer start");

    paint(state);
    return ESP_OK;
}

esp_err_t state_subscribe(esp_event_handler_t handler)
{
    if (!loop) {
        return ESP_ERR_INVALID_STATE;
    }
    return esp_event_handler_instance_register_with(loop, SAT_EVENT, ESP_EVENT_ANY_ID, handler, NULL, NULL);
}

// Never waits: every caller is on a deadline of its own, and a full queue is a bug to see in the log
// rather than a reason to stall one of them.
void state_post(sat_event_id_t ev, const void *data, size_t len)
{
    if (!loop) {
        return;
    }
    if (esp_event_post_to(loop, SAT_EVENT, ev, (void *)data, len, 0) != ESP_OK) {
        ESP_LOGW(TAG, "event %s dropped — queue full", ev_name(ev));
    }
}

void state_report_remote_interrupt(void)
{
    // One step: read-then-clear would let a voice landing between the two be credited to the wrong
    // interrupt.
    int64_t at = atomic_exchange(&heard_at, 0);
    if (at == 0) {
        ESP_LOGI(TAG, "daemon interrupt with no voice heard here");
        return;
    }
    ESP_LOGI(TAG, "daemon interrupt %lld ms after this board heard the voice",
             (esp_timer_get_time() - at) / 1000);
}

sat_state_t state_get(void) { return state; }

bool state_conversation_active(void) { return in_conversation(state); }

const char *state_name(sat_state_t s)
{
    switch (s) {
    case SAT_BOOT:       return "boot";
    case SAT_NO_NETWORK: return "no-network";
    case SAT_NO_DAEMON:  return "no-daemon";
    case SAT_FAULT:      return "fault";
    case SAT_IDLE:       return "idle";
    case SAT_LISTENING:  return "listening";
    case SAT_THINKING:   return "thinking";
    case SAT_SPEAKING:   return "speaking";
    case SAT_APPROVAL:   return "approval";
    case SAT_CAPTURE:    return "capture";
    }
    return "?";
}

static const char *ev_name(sat_event_id_t ev)
{
    switch (ev) {
    case SAT_EV_NET_UP:            return "net-up";
    case SAT_EV_NET_DOWN:          return "net-down";
    case SAT_EV_WAKE:              return "wake";
    case SAT_EV_VOICE:             return "voice";
    case SAT_EV_PLAYBACK_START:    return "playback-start";
    case SAT_EV_PLAYBACK_END:      return "playback-end";
    case SAT_EV_MIC_DEAD:          return "mic-dead";
    case SAT_EV_LINK_UP:           return "link-up";
    case SAT_EV_LINK_DOWN:         return "link-down";
    case SAT_EV_LINK_REJECTED:     return "link-rejected";
    case SAT_EV_UNPROVISIONED:     return "unprovisioned";
    case SAT_EV_REMOTE_STATE:      return "remote-state";
    case SAT_EV_BARGE_IN:          return "barge-in";
    case SAT_EV_SPEECH:            return "speech";
    case SAT_EV_BUTTON_DOWN:       return "button-down";
    case SAT_EV_BUTTON_UP:         return "button-up";
    case SAT_EV_TICK:              return "tick";
    case SAT_EV_CAPTURE_START:     return "capture-start";
    case SAT_EV_CAPTURE_STOP:      return "capture-stop";
    }
    return "?";
}
