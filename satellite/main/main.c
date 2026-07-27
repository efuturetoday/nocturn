// The satellite: a wake word, a microphone, a speaker, and a standing connection to nocturn.
//
// Everything the board hears after the wake word goes up; everything the model says comes back down.
// Both at once — the echo canceller's whole purpose is to let the device listen while it speaks, and
// this is where that finally gets exercised for real. The record-then-replay build that came before
// deliberately never did both, because a loopback feeds the canceller its own output and the better
// it works the quieter the test gets.
//
// Nothing here decides anything. The wake word opens a session and silence closes it; what is said,
// which tools are reachable, and what needs a human's approval are all the daemon's to answer.

#include <string.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "esp_log.h"

#include "audio_out.h"
#include "bsp_board.h"
#include "discover.h"
#include "link.h"
#include "mic_speech.h"
#include "provision.h"
#include "rgb_led_driver.h"
#include "tca9555_driver.h"
#include "uplink.h"
#include "wifi.h"

static const char *TAG = "sat";

// Loud enough to be heard across a room.
#define SPEAKER_VOLUME 85

static uint32_t dropped_up;
static uint32_t dropped_down;
// on_pcm runs on the audio front end's fetch loop, so it only ever queues. Everything expensive —
// the socket, the codec — sits behind a queue precisely so this function stays trivial.
static void on_pcm(const int16_t *pcm, size_t samples, void *user)
{
    if (uplink_write(pcm, samples) != ESP_OK) {
        dropped_up++;
    }
}

// on_event marks the edges of a conversation: the wake word opens one, silence closes it.
static void on_event(mic_speech_event_t event, void *user)
{
    if (event == MIC_EVT_AWAKE) {
        rgb_set_solid(RGB_COLOR_RED);
        audio_out_amp(true);
        uplink_open();
        link_send_text("{\"cmd\":\"voice.wake\",\"ws\":\"main\"}");
        return;
    }
    uplink_close();
    link_send_text("{\"cmd\":\"voice.end\",\"ws\":\"main\"}");
    // Trailing silence before the amplifier drops: with nothing queued the codec repeats its last
    // block, which is audible as a held tone until something else is written.
    audio_out_silence(120);
    vTaskDelay(pdMS_TO_TICKS(200));
    audio_out_amp(false);
    rgb_set_solid(RGB_COLOR_BLUE);
}

// on_audio is speech from the model, already at this board's one rate — the daemon converted it, so
// nothing here resamples.
static void on_audio(const uint8_t *pcm, size_t bytes, void *user)
{
    if (audio_out_write((const int16_t *)pcm, bytes / sizeof(int16_t)) != ESP_OK) {
        dropped_down++;
    }
}

// on_control is what the daemon says outside the audio stream.
//
// Matched by substring rather than parsed: there is exactly one message the board must act on, and a
// JSON parser on the receive path would be more code than the decision it serves. A second message
// makes this a real parse.
static void on_control(const char *json, void *user)
{
    if (strstr(json, "voice.interrupt")) {
        // Barge-in. What is queued answers a question the person has already abandoned — and
        // emptying the queue alone leaves the codec repeating its last block as a held tone.
        audio_out_flush();
        audio_out_silence(60);
        ESP_LOGI(TAG, "interrupted");
        return;
    }
    ESP_LOGI(TAG, "daemon: %s", json);
}

// report is the only instrumentation, and it prints unconditionally.
//
// The console is the chip's native USB, so every reset re-enumerates the device and kills whatever
// held the port: firmware that only speaks when something is wrong is indistinguishable from
// firmware that is dead. The drop counters are the numbers that matter here — they are how a link
// that cannot keep up announces itself.
static void report(void *arg)
{
    for (;;) {
        vTaskDelay(pdMS_TO_TICKS(3000));
        uint32_t chunks = 0, samples = 0, dry = 0;
        int werr = 0;
        audio_out_stats(&chunks, &samples, &werr, &dry);
        ESP_LOGI(TAG, "alive — link=%d session=%d peak=%ld up_drop=%lu down_drop=%lu played=%lu/%lu dry=%lu werr=%d",
                 link_connected(), mic_speech_session_open(), (long)mic_speech_peak(),
                 dropped_up, dropped_down, chunks, samples, dry, werr);
        dropped_up = 0;
        dropped_down = 0;
    }
}

// network brings up the link and leaves it up.
//
// Its own task, because discovery waits on the network and a board that answers no wake word for
// several seconds looks broken. The audio path starts first and works without a daemon; the link
// joins it when it can.
static void network(void *arg)
{
    provision_t prov;
    if (provision_load(&prov) != ESP_OK) {
        ESP_LOGE(TAG, "not provisioned — flash an NVS image with ssid, pass and bearer");
        rgb_set_solid(RGB_COLOR_RED);
        vTaskDelete(NULL);
        return;
    }

    ESP_ERROR_CHECK(wifi_start(prov.ssid, prov.pass));
    if (!wifi_wait(30000)) {
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
    // clock, so the board has exactly one rate. The daemon converts the model's 24 kHz for us, which
    // is why nothing here resamples.
    ESP_ERROR_CHECK(esp_board_init(16000, 2, 32));
    tca9555_driver_init();
    // RGB_Example, not configure_led: the latter returns a handle and assigns it to a LOCAL, so the
    // static one set_rgb_color posts against stays null — along with the queue it posts to.
    RGB_Example();
    rgb_set_solid(RGB_COLOR_BLUE);
    esp_audio_set_play_vol(SPEAKER_VOLUME);

    ESP_ERROR_CHECK(audio_out_init());
    ESP_ERROR_CHECK(uplink_start());
    ESP_ERROR_CHECK(mic_speech_start(on_pcm, on_event, NULL));

    xTaskCreate(report, "report", 3 * 1024, NULL, 2, NULL);
    xTaskCreate(network, "network", 5 * 1024, NULL, 3, NULL);

    ESP_LOGI(TAG, "ready — say the wake word and talk");
}
