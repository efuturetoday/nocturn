// Step two of bringing the satellite up: prove the acoustics before any networking exists.
//
// After the wake word, the cleaned microphone audio goes straight back out of the speaker. That
// makes the one question this hardware has to answer audible: with the board playing and listening
// at the same time, does the echo canceller hold, or does it hear itself and run away? Every later
// stage — WebSocket, daemon, live model — assumes it holds, and finding that out here costs an
// afternoon instead of a fortnight.
//
// The loopback is also the seam. Replacing this one sink with "send upstream" is what turns this
// into a real satellite; nothing else about the audio path changes.

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "esp_log.h"
#include "esp_system.h"
#include <string.h>
#include "esp_heap_caps.h"
#include <string.h>

#include "audio_out.h"
#include "bsp_board.h"
#include "button.h"
#include "discover.h"
#include "link.h"
#include "mic_speech.h"
#include "provision.h"
#include "rgb_led_driver.h"
#include "state.h"
#include "tca9555_driver.h"
#include "uplink.h"
#include "wifi.h"

static const char *TAG = "sat";

// Loud enough to be audible across a room, quiet enough not to invite howling while the canceller
// is the thing under test.
#define TEST_VOLUME 85

// One press of the volume buttons. Seven steps to cross the useful range, which is roughly what the
// ring can show anyway — a finer step would move the arc by less than a pixel and read as nothing
// happening.
#define VOLUME_STEP 15
#define VOLUME_MIN 10

static int volume = TEST_VOLUME;

// set_volume applies a change and shows it, which are one action from the person's side.
//
// The ring is the only feedback this device has, and volume is the one setting where the result is
// not audible until the next reply — so it has to be visible now, or the press feels like it did
// nothing and gets repeated.
static void set_volume(int delta)
{
    volume += delta;
    if (volume > 100) {
        volume = 100;
    }
    if (volume < VOLUME_MIN) {
        volume = VOLUME_MIN;
    }
    esp_audio_set_play_vol(volume);
    // Amber, which is nothing else's colour: a state is being described nowhere on the ring while
    // this shows, and it must not be mistakable for one.
    rgb_gauge(volume, RGB_AMBER);
    ESP_LOGI(TAG, "volume %d%%", volume);
}

// Record first, play afterwards — never both at once.
//
// A live loopback cannot answer whether the microphone works, because the echo canceller exists
// precisely to remove the speaker from the microphone: feeding the speaker with what the microphone
// just heard gives the canceller its own output to subtract, and the better it works the quieter the
// test gets. Separating the two in time takes it out of the picture entirely — nothing is playing
// while we capture, and nothing is captured while we play.
#define CAPTURE_SECONDS 5
#define CAPTURE_SAMPLES (16000 * CAPTURE_SECONDS)

static int16_t *capture;
static size_t captured;
static volatile bool playing_back;

// Whether the daemon has acknowledged the session this board asked for. See on_control.
static volatile bool confirmed;

// Frames the uplink could not take, and speech the playback queue could not.
static uint32_t dropped_up;
static uint32_t dropped_down;
// Speech received from the daemon since the last report. The counterpart to the uplink's: a queue
// that runs dry has two causes — nothing arriving, or arriving too late — and only comparing what
// came in against what was played tells them apart.
static uint32_t down_bytes;

// Peak amplitude of what the front end handed us since the last report. Silence has to be measured
// at the source: everything downstream can be provably working and still play nothing if the
// samples arriving here are zeros.
static volatile int32_t peak;

// on_pcm runs on the fetch loop, so it only queues. Drops are counted rather than logged: printing
// from here would be the very stall the queue exists to prevent.
static void on_pcm(const int16_t *pcm, size_t samples, void *user)
{
    int32_t p = peak;
    for (size_t i = 0; i < samples; i++) {
        int32_t v = pcm[i] < 0 ? -pcm[i] : pcm[i];
        if (v > p) {
            p = v;
        }
    }
    peak = p;

    // Upstream FIRST, and before any early return below. This is the real path; the capture that
    // follows is a bench tool sharing the same samples, and a tool must not be able to silence the
    // product by returning early.
    if (uplink_write(pcm, samples) != ESP_OK) {
        dropped_up++;
    }

    // While a hand-held recording runs, capture regardless of the wake word: the point is to hear
    // what the microphone path produces, and that must not depend on the part being debugged.
    if (playing_back || captured >= CAPTURE_SAMPLES) {
        return;
    }
    size_t room = CAPTURE_SAMPLES - captured;
    size_t take = samples < room ? samples : room;
    memcpy(&capture[captured], pcm, take * sizeof(int16_t));
    captured += take;
}

// Frames the uplink could not take, and speech the playback queue could not.
// How many times voice activity was detected while the speaker was running.
//
// This is the barge-in question asked for free. During replay the only voice in the room is the
// board's own, coming out of its own speaker — so every count here is the echo canceller failing to
// remove it, and a barge-in built on this detector would interrupt the assistant on its own voice.
// Which is exactly what the full-duplex build did: 226 ms into every reply.
//
// Zero counts across a replay the person stayed quiet through is the permission to build barge-in.
// Anything else says the acoustics have to be fixed first, and no amount of protocol will help.
static uint32_t voice_while_playing;

// Hold to record, release to hear it back.
//
// The wake word is a poor bench tool: it needs a quiet room to trigger, it decides for itself when
// the utterance ended, and it is one of the things under test. Holding a button decides none of
// that, which is what listening to a gain change or an echo-cancellation setting actually needs.
//
// Held rather than toggled, because the button IS the state. A toggle needs the person to know which
// half of it they are in, and a device with seven LEDs and no screen has no good way to tell them —
// so they press, wonder, and press again to find out, which stops the recording they just started.
static volatile bool hand_recording;

// on_sat drives the recording, and nothing else. The ring belongs to the state module now — this
// used to paint colours here, which is how an unprovisioned board and a listening one ended up
// looking identical.
static void on_sat(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    switch (id) {
    case SAT_EV_BUTTON_DOWN:
        if (*(button_id_t *)data == BUTTON_B) {
            set_volume(-VOLUME_STEP);
            return;
        }
        if (*(button_id_t *)data == BUTTON_C) {
            set_volume(+VOLUME_STEP);
            return;
        }
        if (*(button_id_t *)data != BUTTON_A) {
            return;
        }
        hand_recording = true;
        captured = 0;
        mic_speech_hold(true);
        ESP_LOGI(TAG, "button held — recording");
        return;
    case SAT_EV_BUTTON_UP:
        if (*(button_id_t *)data != BUTTON_A || !hand_recording) {
            return;
        }
        hand_recording = false;
        mic_speech_hold(false);
        playing_back = true;
        ESP_LOGI(TAG, "button released — replaying");
        return;
    case SAT_EV_WAKE:
        captured = 0;
        confirmed = false;
        uplink_open();
        // Hold the microphone open for the whole conversation, not for one utterance.
        //
        // mic_speech closes its own sink after 900 ms of silence, which is the right answer for
        // "when did this person stop talking" and the wrong one for "should we still be listening".
        // Full duplex means the stream never stops: it is how the model works out for itself that a
        // turn ended, and it is the only reason a barge-in can be heard at all — talking over a reply
        // is by definition talking while no utterance of ours is open.
        mic_speech_hold(true);
        // Both sides count a session from zero. Whatever is left over from the last one would
        // otherwise describe a conversation the daemon has already forgotten.
        audio_out_flush();
        audio_out_take_freed();
        audio_out_amp(true);
        link_send_text("{\"cmd\":\"voice.wake\",\"ws\":\"main\"}");
        {
            char msg[64];
            snprintf(msg, sizeof(msg), "{\"cmd\":\"voice.credit\",\"ws\":\"main\",\"bytes\":%u}",
                     (unsigned)audio_out_capacity());
            link_send_text(msg);
        }
        return;
    case SAT_EV_VOICE:
        if (playing_back) {
            voice_while_playing++;
        }
        return;
    case SAT_EV_SPEECH:
        link_send_text("{\"cmd\":\"voice.speech\",\"ws\":\"main\"}");
        return;
    case SAT_EV_BARGE_IN:
        // Instant mute, and that is the whole local job. Everything queued answers a question the
        // person has already abandoned, and it is the one part of this that must not wait for a round
        // trip — the model works out for itself that it has been interrupted, since the microphone
        // never stopped streaming to it.
        audio_out_flush();
        link_send_text("{\"cmd\":\"voice.barge\",\"ws\":\"main\"}");
        return;
    case SAT_EV_UTTERANCE_END:
        // Deliberately nothing.
        //
        // This is the end of an UTTERANCE, not of the conversation, and it used to close the uplink
        // and send voice.end — which tore the model session down 900 ms after the person stopped
        // talking, before it could answer. Every reply came back as "messages: 0".
        //
        // The model decides for itself when someone has finished speaking; that is what it needs the
        // stream for. And closing the uplink here would make barge-in impossible, since talking over
        // a reply is by definition talking while no utterance of ours is open.
        //
        // The conversation ends when the DAEMON says so — see on_control.
        return;
    default:
        return;
    }
}

// playback_task waits for a finished recording and plays it. It runs off the audio path so the
// front end keeps feeding while the speaker works.
static void playback_task(void *arg)
{
    for (;;) {
        if (!playing_back) {
            vTaskDelay(pdMS_TO_TICKS(50));
            continue;
        }
        size_t n = captured;
        ESP_LOGI(TAG, "replaying %u samples (%u ms)", (unsigned)n, (unsigned)(n * 1000 / 16000));
        // BEFORE the amplifier, not after. Raising it is what clicks, and the window that discards
        // that click only opens once this has been seen.
        esp_event_post(SAT_EVENT, SAT_EV_PLAYBACK_START, NULL, 0, portMAX_DELAY);
        audio_out_amp(true);

        // In chunks, so the queue is never asked for more than it holds.
        const size_t step = 1024;
        for (size_t off = 0; off < n; off += step) {
            size_t take = (n - off) < step ? (n - off) : step;
            while (audio_out_write(&capture[off], take) != ESP_OK) {
                vTaskDelay(pdMS_TO_TICKS(10)); // queue full: wait rather than drop, this is a test
            }
        }
        // Trailing silence, then let it all drain. Without the zeros the codec sits on its last
        // buffer and holds the final sample as a tone until something else is written.
        audio_out_silence(120);
        vTaskDelay(pdMS_TO_TICKS(n * 1000 / 16000 + 600));
        audio_out_amp(false);
        esp_event_post(SAT_EVENT, SAT_EV_PLAYBACK_END, NULL, 0, 0);
        captured = 0;
        playing_back = false;
        ESP_LOGI(TAG, "replay done");
    }
}

// report is the loopback's instrumentation, and it prints unconditionally on purpose.
//
// The console is the chip's native USB, so every reset re-enumerates the device and kills whatever
// had the port open. Attaching afterwards means the boot banner is long gone, and firmware that only
// speaks when something is wrong is indistinguishable from firmware that is dead. A heartbeat makes
// the state readable at any moment, without having to catch a reset.
static void report(void *arg)
{
    for (;;) {
        vTaskDelay(pdMS_TO_TICKS(3000));
        uint32_t chunks = 0, samples = 0;
        int werr = 0;
        audio_out_stats(&chunks, &samples, &werr);
        uint32_t up_bytes = 0, up_fails = 0;
        uplink_stats(&up_bytes, &up_fails);
        ESP_LOGI(TAG, "alive — %s voice=%d self_trig=%lu peak=%ld up=%lums down=%lums played=%lums depth=%ums up_fail=%lu drop=%lu/%lu werr=%d",
                 state_name(state_get()), mic_speech_voice(), voice_while_playing, (long)peak,
                 up_bytes / 32, down_bytes / 32, samples / 16,
                 (unsigned)(audio_out_depth() * 1000 / 32000), up_fails, dropped_up, dropped_down, werr);
        down_bytes = 0;
        (void)chunks;
        peak = 0;
        dropped_up = 0;
        dropped_down = 0;
    }
}

// on_control is what the daemon says outside the audio stream.
//
// Matched by substring rather than parsed: there is exactly one message the board must act on, and a
// JSON parser on the receive path would be more code than the decision it serves. A second message
// makes this a real parse.
static void on_control(const char *json, void *user)
{
    // The daemon owns the end of a conversation: it is the side that knows whether the model is done,
    // whether a tool is still running, and what the session costs to hold open. The board only knows
    // that a person stopped making noise, which is a different question.
    //
    // But voice.state carries no session identity, so an "idle" cannot be attributed to the session
    // it came from. The previous conversation's ending arrives AFTER the wake word that started the
    // next one, and closed it again a fraction of a second after it opened — measured.
    //
    // So a wake word waits for its acknowledgement: "listening" is the daemon confirming the session
    // this board just asked for, and until it arrives every "idle" belongs to a conversation that is
    // already over.
    if (strstr(json, "\"state\":\"listening\"")) {
        confirmed = true;
        return;
    }
    if (confirmed && strstr(json, "\"state\":\"idle\"")) {
        confirmed = false;
        mic_speech_hold(false);
        uplink_close();
        audio_out_amp(false);
        ESP_LOGI(TAG, "conversation over");
        return;
    }
    if (strstr(json, "voice.interrupt")) {
        state_report_remote_interrupt();
        // The daemon agreeing with a barge-in this board already made, or making one of its own.
        // Either way what is queued answers a question already abandoned.
        audio_out_flush();
        ESP_LOGI(TAG, "interrupted");
        return;
    }
    ESP_LOGI(TAG, "daemon: %s", json);
}

// on_audio is speech from the model, already at this board's one rate — the daemon converts it, so
// nothing here resamples.
//
// It runs on the WebSocket task and must not block, which is why the queue behind it never waits:
// this task also drives the keepalive, and a wait longer than the client's read timeout looks to the
// client like a dead socket. It answers by dropping the connection, which was measured.
static void on_audio(const uint8_t *pcm, size_t bytes, void *user)
{
    down_bytes += bytes;
    if (audio_out_write((const int16_t *)pcm, bytes / sizeof(int16_t)) != ESP_OK) {
        // Should be impossible: the sender only has as much outstanding as this queue can hold. A
        // count here means the credit accounting is wrong, not that the network was fast.
        dropped_down++;
    }
}

// How often credit is returned. The interval is dead time added to every reply that fills the queue,
// so it is short — but the queue is ten times the round trip, so nothing here is load-bearing.
#define CREDIT_MS 100

// credit_task hands back what playback has consumed.
//
// This IS the flow control, and it is the only one: the daemon may have audio_out_capacity() bytes
// outstanding, and every byte the speaker emits earns it one more. Overflow is structurally
// impossible rather than merely unlikely, and nothing anywhere has to block to achieve it.
static void credit_task(void *arg)
{
    for (;;) {
        vTaskDelay(pdMS_TO_TICKS(CREDIT_MS));
        size_t n = audio_out_take_freed();
        if (n == 0 || !link_connected()) {
            continue;
        }
        char msg[64];
        snprintf(msg, sizeof(msg), "{\"cmd\":\"voice.credit\",\"ws\":\"main\",\"bytes\":%u}", (unsigned)n);
        link_send_text(msg);
    }
}

// network brings up the link and leaves it up.
//
// It runs as its own task rather than inline in app_main: discovery waits on the network, and a
// board that shows no LED and answers no wake word for several seconds looks broken. The audio path
// starts first and works without a daemon; the link joins it when it can.
static void network(void *arg)
{
    provision_t prov;
    if (provision_load(&prov) != ESP_OK) {
        ESP_LOGE(TAG, "not provisioned — flash an NVS image with ssid, pass and bearer");
        esp_event_post(SAT_EVENT, SAT_EV_UNPROVISIONED, NULL, 0, 0);
        vTaskDelete(NULL);
        return;
    }

    ESP_ERROR_CHECK(wifi_start(prov.ssid, prov.pass));
    if (!wifi_wait(30000)) {
        // Not fatal: wifi keeps retrying on its own, and discovery below will simply fail until it
        // succeeds. Saying so is worth more than giving up.
        ESP_LOGW(TAG, "no network yet, still trying");
    }

    daemon_addr_t daemon = {0};
    if (prov.host[0]) {
        strlcpy(daemon.host, prov.host, sizeof(daemon.host));
        daemon.port = prov.port;
        strlcpy(daemon.path, "/ws", sizeof(daemon.path));
    } else {
        discover_init();
        while (!discover_find(&daemon, 3000)) {
            ESP_LOGW(TAG, "nocturn not found, retrying");
            vTaskDelay(pdMS_TO_TICKS(5000));
        }
    }

    ESP_ERROR_CHECK(link_start(daemon.host, daemon.port, daemon.path, prov.bearer,
                               on_control, on_audio, NULL));
    vTaskDelete(NULL);
}

void app_main(void)
{
    // 16 kHz throughout: the capture path, the front end and the playback reference share the codec
    // clock, so the board has exactly one rate. Audio arriving at another rate is resampled
    // upstream, never here.
    ESP_LOGI(TAG, "step: board");
    ESP_ERROR_CHECK(esp_board_init(16000, 2, 32)); // match the bus: 32-bit stereo slots
    ESP_LOGI(TAG, "step: tca");
    tca9555_driver_init();
    // The shared things are created HERE, by the one place that can order them, and handed to the
    // modules that use them. No module creates what it merely needs first — wifi.c and state.c both
    // creating the event loop is how this became a boot loop whose message pointed at neither.
    ESP_LOGI(TAG, "step: event loop");
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    ESP_LOGI(TAG, "step: rgb");
    ESP_ERROR_CHECK(rgb_start());
    ESP_LOGI(TAG, "step: state");
    // Before any source can post: an event delivered with nobody listening is a transition the ring
    // never learns about.
    ESP_ERROR_CHECK(state_start());
    ESP_ERROR_CHECK(esp_event_handler_instance_register(SAT_EVENT, ESP_EVENT_ANY_ID, on_sat, NULL, NULL));
    ESP_LOGI(TAG, "step: volume");
    esp_audio_set_play_vol(volume);
    ESP_LOGI(TAG, "step: audio_out");
    capture = heap_caps_malloc(CAPTURE_SAMPLES * sizeof(int16_t), MALLOC_CAP_SPIRAM);
    ESP_ERROR_CHECK(capture ? ESP_OK : ESP_ERR_NO_MEM);

    ESP_ERROR_CHECK(audio_out_init());
    ESP_LOGI(TAG, "step: mic");
    ESP_ERROR_CHECK(mic_speech_start(on_pcm, NULL));
    ESP_ERROR_CHECK(button_start());
    ESP_ERROR_CHECK(uplink_start());
    xTaskCreate(credit_task, "credit", 3 * 1024, NULL, 3, NULL);
    ESP_LOGI(TAG, "step: done");

    xTaskCreate(report, "report", 3 * 1024, NULL, 2, NULL);
    xTaskCreate(network, "network", 5 * 1024, NULL, 3, NULL);
    xTaskCreate(playback_task, "replay", 4 * 1024, NULL, 4, NULL);

    ESP_LOGI(TAG, "ready — wake word, talk for up to %d s, then it replays", CAPTURE_SECONDS);
}
