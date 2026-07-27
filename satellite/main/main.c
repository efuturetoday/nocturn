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
#include "wifi.h"

static const char *TAG = "sat";

// Loud enough to be audible across a room, quiet enough not to invite howling while the canceller
// is the thing under test.
#define TEST_VOLUME 85

static uint32_t dropped;

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
        return;
    case SAT_EV_VOICE:
        if (playing_back) {
            voice_while_playing++;
        }
        return;
    case SAT_EV_UTTERANCE_END:
        // A hand-held recording outlasts the front end's idea of an utterance: it ends when the
        // button says so, not when someone happens to pause.
        if (!hand_recording) {
            playing_back = true;
        }
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
        ESP_LOGI(TAG, "alive — %s voice=%d self_trig=%lu peak=%ld queued_drop=%lu played=%lu chunks/%lu samples werr=%d",
                 state_name(state_get()), mic_speech_voice(), voice_while_playing,
                 (long)peak, dropped, chunks, samples, werr);
        peak = 0;
        dropped = 0;
    }
}

// on_control is what the daemon says. Nothing acts on it yet: this build exists to prove the
// connection, and interpreting messages before the transport is trusted only makes a failure harder
// to place.
static void on_control(const char *json, void *user)
{
    ESP_LOGI(TAG, "daemon: %s", json);
}

// on_audio would go to the speaker. It counts instead, for the same reason.
static void on_audio(const uint8_t *pcm, size_t bytes, void *user)
{
    ESP_LOGI(TAG, "daemon sent %u bytes of speech", (unsigned)bytes);
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
    esp_audio_set_play_vol(TEST_VOLUME);
    ESP_LOGI(TAG, "step: audio_out");
    capture = heap_caps_malloc(CAPTURE_SAMPLES * sizeof(int16_t), MALLOC_CAP_SPIRAM);
    ESP_ERROR_CHECK(capture ? ESP_OK : ESP_ERR_NO_MEM);

    ESP_ERROR_CHECK(audio_out_init());
    ESP_LOGI(TAG, "step: mic");
    ESP_ERROR_CHECK(mic_speech_start(on_pcm, NULL));
    ESP_ERROR_CHECK(button_start());
    ESP_LOGI(TAG, "step: done");

    xTaskCreate(report, "report", 3 * 1024, NULL, 2, NULL);
    xTaskCreate(network, "network", 5 * 1024, NULL, 3, NULL);
    xTaskCreate(playback_task, "replay", 4 * 1024, NULL, 4, NULL);

    ESP_LOGI(TAG, "ready — wake word, talk for up to %d s, then it replays", CAPTURE_SECONDS);
}
