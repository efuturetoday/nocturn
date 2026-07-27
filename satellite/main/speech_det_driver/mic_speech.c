#include "mic_speech.h"

#include "esp_afe_config.h"
#include "esp_afe_sr_iface.h"
#include "esp_afe_sr_models.h"
#include "model_path.h"

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "bsp_board.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_task_wdt.h"

static const char *TAG = "sat/mic";

// How long silence must hold before an utterance counts as finished. Short enough that the person
// is not left waiting after they stop, long enough to survive the pause in the middle of a
// sentence. The front end's own VAD supplies the per-frame decision; this only debounces it.
#define SILENCE_TO_END_MS 900

static esp_afe_sr_iface_t *afe_handle;
static esp_afe_sr_data_t *afe_data;
static volatile bool running;
static volatile bool session;

static mic_pcm_sink_t pcm_sink;
static mic_speech_event_cb_t event_cb;
static void *cb_user;

static void emit(mic_speech_event_t ev)
{
    if (event_cb) {
        event_cb(ev, cb_user);
    }
}

static void feed_task(void *arg)
{
    int chunk = afe_handle->get_feed_chunksize(afe_data);
    int channels = esp_get_feed_channel();
    assert(afe_handle->get_feed_channel_num(afe_data) == channels);

    int16_t *buf = heap_caps_malloc(chunk * sizeof(int16_t) * channels, MALLOC_CAP_SPIRAM);
    assert(buf);

    esp_task_wdt_add(NULL);
    while (running) {
        // Raw channels: the codec presents them in the layout esp_get_input_format() declares
        // ("RMNM" — a playback reference alongside the microphones), and the front end unpacks it
        // itself. That reference is what makes echo cancellation possible without any clock work of
        // ours, so it must reach the front end untouched.
        esp_get_feed_data(true, buf, chunk * sizeof(int16_t) * channels);
        afe_handle->feed(afe_data, buf);
        esp_task_wdt_reset();
    }
    free(buf);
    vTaskDelete(NULL);
}

static void detect_task(void *arg)
{
    int silence_ms = 0;
    int frame_ms = 0;

    esp_task_wdt_add(NULL);
    while (running) {
        afe_fetch_result_t *res = afe_handle->fetch(afe_data);
        if (!res || res->ret_value == ESP_FAIL) {
            ESP_LOGE(TAG, "fetch failed");
            break;
        }
        if (!frame_ms) {
            // Derived rather than assumed: the chunk size is the front end's to choose.
            frame_ms = (res->data_size / (int)sizeof(int16_t)) * 1000 / 16000;
            ESP_LOGI(TAG, "listening — %d ms frames", frame_ms);
        }

        // A multi-channel front end reports the wake word twice: once on detection, then again once
        // it has decided which microphone heard it. Waiting for the verified one avoids opening a
        // session on the array's guess.
        bool woke = res->raw_data_channels == 1 ? res->wakeup_state == WAKENET_DETECTED
                                                : res->wakeup_state == WAKENET_CHANNEL_VERIFIED;
        if (woke && !session) {
            session = true;
            silence_ms = 0;
            emit(MIC_EVT_AWAKE);
            // The front end holds back the audio from just before the trigger. Without it the
            // first word of the request is missing — the person says "nocturn, what time is it"
            // and the far side receives "at time is it".
            if (res->vad_cache_size > 0 && pcm_sink) {
                pcm_sink(res->vad_cache, res->vad_cache_size / sizeof(int16_t), cb_user);
            }
        }

        if (session) {
            if (pcm_sink) {
                pcm_sink(res->data, res->data_size / sizeof(int16_t), cb_user);
            }
            silence_ms = res->vad_state == VAD_SPEECH ? 0 : silence_ms + frame_ms;
            if (silence_ms >= SILENCE_TO_END_MS) {
                session = false;
                emit(MIC_EVT_SPEECH_END);
                // Wakenet is suppressed while a session runs, so the wake word inside a sentence
                // does not restart it. Re-arm now that the session is over.
                afe_handle->enable_wakenet(afe_data);
            }
        }
        esp_task_wdt_reset();
    }
    ESP_LOGW(TAG, "detect loop exited");
    vTaskDelete(NULL);
}

esp_err_t mic_speech_start(mic_pcm_sink_t sink, mic_speech_event_cb_t on_event, void *user)
{
    pcm_sink = sink;
    event_cb = on_event;
    cb_user = user;

    srmodel_list_t *models = esp_srmodel_init("model"); // partition label from partitions.csv
    afe_config_t *cfg = afe_config_init(esp_get_input_format(), models, AFE_TYPE_SR, AFE_MODE_LOW_COST);
    if (!cfg) {
        return ESP_ERR_NO_MEM;
    }

    // Stated rather than inherited. The vendor demo left noise suppression and voice activity off
    // because a command model does not need either; a satellite needs both — the far side hears
    // whatever we send, and something has to decide when a sentence ended.
    cfg->aec_init = true;
    cfg->ns_init = true;
    cfg->vad_init = true;
    cfg->vad_mode = VAD_MODE_1;
    cfg->vad_min_speech_ms = 128;
    cfg->vad_min_noise_ms = 500;

    // Automatic gain, which the vendor never switched on because a command model does not need it.
    // A satellite does: the person is across the room, not holding the board, and whatever leaves
    // here is what the far side has to understand. Without this the output is technically correct
    // and practically inaudible.
    cfg->agc_init = true;
    cfg->agc_mode = AFE_AGC_MODE_WEBRTC;

    // A fixed multiplier on top, because AGC only tracks — it does not raise a quiet room to a
    // usable level on its own. Conservative: this scales the amplitude directly, so too much
    // clips before it gets louder.
    cfg->afe_linear_gain = 3.0f;

    afe_handle = esp_afe_handle_from_config(cfg);
    afe_data = afe_handle->create_from_config(cfg);
    afe_config_free(cfg);
    if (!afe_data) {
        return ESP_ERR_NO_MEM;
    }

    running = true;
    // Opposite cores: feeding is I2S-bound and fetching is compute-bound, and letting them share a
    // core reintroduces exactly the stall the queueing elsewhere exists to avoid.
    xTaskCreatePinnedToCore(detect_task, "mic_detect", 8 * 1024, NULL, 5, NULL, 1);
    xTaskCreatePinnedToCore(feed_task, "mic_feed", 8 * 1024, NULL, 5, NULL, 0);
    return ESP_OK;
}

bool mic_speech_session_open(void) { return session; }
