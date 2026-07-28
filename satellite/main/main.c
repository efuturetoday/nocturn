// The process spine: bring the parts up in the one order that works, then speak the protocol.
//
// It owns no audio and decides nothing about a conversation — the ring belongs to state/, the
// microphone's past to micbuf/, the speaker to audio_out/, the bench loopback to bench/.

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include <stdatomic.h>
#include <string.h>

#include "esp_log.h"
#include "esp_system.h"

#include "audio_out.h"
#include "bench.h"
#include "bsp_board.h"
#include "button.h"
#include "discover.h"
#include "link.h"
#include "mic_speech.h"
#include "micbuf.h"
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

// Speech the playback queue could not take, and speech received since the last report. A queue that
// runs dry has two causes — nothing arriving, or arriving too late — and only comparing the two
// tells them apart. Written on the WebSocket task, read and cleared on the report task.
static atomic_uint_least32_t dropped_down;
static atomic_uint_least32_t down_bytes;

// on_sat is the protocol side of what the board observes: telling the daemon, and nothing else.
static void on_sat(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    switch (id) {
    case SAT_EV_BUTTON_DOWN:
        if (*(button_id_t *)data == BUTTON_B) {
            set_volume(-VOLUME_STEP);
        } else if (*(button_id_t *)data == BUTTON_C) {
            set_volume(+VOLUME_STEP);
        }
        return; // button A is the bench tool's, and it is bench/'s to answer
    case SAT_EV_WAKE:
        link_send_text("{\"cmd\":\"voice.wake\",\"ws\":\"main\"}");
        {
            char msg[64];
            snprintf(msg, sizeof(msg), "{\"cmd\":\"voice.credit\",\"ws\":\"main\",\"bytes\":%u}",
                     (unsigned)audio_out_capacity());
            link_send_text(msg);
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
    default:
        return;
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
        uint32_t up_bytes = 0, up_fails = 0, up_late = 0;
        uplink_stats(&up_bytes, &up_fails, &up_late);
        mic_stats_t mic;
        mic_speech_stats(&mic);
        // Read left to right: fetch=0 means the loop is not turning and nothing after it describes
        // the present; then peak says whether the microphone delivers, speech whether the detector
        // agrees, armed whether the wake word is listening at all.
        ESP_LOGI(TAG, "mic — fetch=%lu/%lu speech=%lu wake=%lu ref=%ld raw=%ld clean=%ld armed=%d voice=%d",
                 mic.fetches, mic.samples, mic.speech, mic.wakes, (long)mic.ref_peak,
                 (long)mic.raw_peak, (long)micbuf_take_peak(), mic.armed, mic_speech_voice());
        ESP_LOGI(TAG, "alive — %s up=%lums down=%lums played=%lums depth=%ums up_fail=%lu late=%lu drop=%lu werr=%d",
                 state_name(state_get()),
                 up_bytes / 32, atomic_exchange(&down_bytes, 0) / 32, samples / 16,
                 (unsigned)(audio_out_depth() * 1000 / 32000), up_fails, up_late,
                 atomic_exchange(&dropped_down, 0), werr);
        (void)chunks;
    }
}

// state_field lifts the value of "state" out of a daemon message. Extracted rather than matched, so
// one place answers "which state" and the meaning stays in state/.
//
// Not a JSON parser: the board reads exactly one field, and a parser on the receive path would be
// more code than the decision it serves.
static bool state_field(const char *json, char *out, size_t len)
{
    static const char key[] = "\"state\":\"";
    const char *at = strstr(json, key);
    if (!at) {
        return false;
    }
    at += sizeof(key) - 1;
    const char *end = strchr(at, '"');
    if (!end || (size_t)(end - at) >= len) {
        return false;
    }
    memcpy(out, at, end - at);
    out[end - at] = '\0';
    return true;
}

// on_control is what the daemon says outside the audio stream. It forwards and does not interpret:
// the daemon owns the shape of a conversation, state/ owns what that means here.
static void on_control(const char *json, void *user)
{
    char name[16];
    if (state_field(json, name, sizeof(name))) {
        state_post(SAT_EV_REMOTE_STATE, name, strlen(name) + 1);
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
    atomic_fetch_add(&down_bytes, bytes);
    if (audio_out_write((const int16_t *)pcm, bytes / sizeof(int16_t)) != ESP_OK) {
        // Should be impossible: the sender only has as much outstanding as this queue can hold. A
        // count here means the credit accounting is wrong, not that the network was fast.
        atomic_fetch_add(&dropped_down, 1);
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
        state_post(SAT_EV_UNPROVISIONED, NULL, 0);
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
        ESP_ERROR_CHECK(discover_init());
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
    ESP_ERROR_CHECK(esp_board_init()); // the bus format is the board's, not a parameter
    ESP_LOGI(TAG, "step: tca");
    ESP_ERROR_CHECK(tca9555_driver_init());
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
    ESP_ERROR_CHECK(state_subscribe(on_sat));
    ESP_LOGI(TAG, "step: volume");
    esp_audio_set_play_vol(volume);
    ESP_LOGI(TAG, "step: audio_out");
    ESP_ERROR_CHECK(audio_out_init());
    ESP_LOGI(TAG, "step: micbuf");
    ESP_ERROR_CHECK(micbuf_init()); // before the front end, which writes to it on its first frame
    ESP_LOGI(TAG, "step: mic");
    ESP_ERROR_CHECK(mic_speech_start());
    ESP_ERROR_CHECK(button_start());
    ESP_ERROR_CHECK(uplink_start());
    ESP_ERROR_CHECK(bench_start());
    xTaskCreate(credit_task, "credit", 3 * 1024, NULL, 3, NULL);
    ESP_LOGI(TAG, "step: done");

    xTaskCreate(report, "report", 3 * 1024, NULL, 2, NULL);
    xTaskCreate(network, "network", 5 * 1024, NULL, 3, NULL);

    ESP_LOGI(TAG, "ready — say the wake word, or hold button A to hear the microphone back");
}
