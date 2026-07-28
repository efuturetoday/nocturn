#include "bench.h"

#include <assert.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "esp_check.h"
#include "esp_heap_caps.h"
#include "esp_log.h"

#include "audio_out.h"
#include "button.h"
#include "micbuf.h"
#include "rgb_led_driver.h"
#include "state.h"

static const char *TAG = "sat/bench";

#define BLOCK_SAMPLES 512

static micbuf_reader_t reader;
static volatile bool recording;
static volatile bool replaying;

// Where the recording ends. The microphone never stops, so a replay that read until nothing was left
// would chase the writer forever.
static volatile uint32_t until;

static void on_sat(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (id == SAT_EV_BUTTON_DOWN) {
        if (*(button_id_t *)data != BUTTON_A || recording || replaying) {
            return;
        }
        // About the speaker, not the microphone: a replay over a running reply collides.
        if (state_conversation_active()) {
            rgb_flash(RGB_RED);
            ESP_LOGW(TAG, "not now — a conversation is running");
            return;
        }
        micbuf_attach(&reader, 0); // the press is the start
        recording = true;
        ESP_LOGI(TAG, "button held — recording");
        return;
    }
    if (id == SAT_EV_BUTTON_UP) {
        if (*(button_id_t *)data != BUTTON_A || !recording) {
            return;
        }
        recording = false;
        until = micbuf_written();
        replaying = true;
        ESP_LOGI(TAG, "button released — replaying");
    }
}

static void replay_task(void *arg)
{
    int16_t *block = heap_caps_malloc(BLOCK_SAMPLES * sizeof(int16_t), MALLOC_CAP_SPIRAM);
    assert(block);

    for (;;) {
        if (!replaying) {
            vTaskDelay(pdMS_TO_TICKS(50));
            continue;
        }
        uint32_t total = until - reader.cursor;
        ESP_LOGI(TAG, "replaying %u samples (%u ms)", (unsigned)total,
                 (unsigned)(total * 1000 / 16000));
        audio_out_amp(true);

        for (;;) {
            uint32_t left = until - reader.cursor;
            if (left == 0) {
                break;
            }
            size_t want = left < BLOCK_SAMPLES ? left : BLOCK_SAMPLES;
            size_t n = micbuf_read(&reader, block, want, 0);
            if (n == 0) {
                break;
            }
            while (audio_out_write(block, n) != ESP_OK) {
                vTaskDelay(pdMS_TO_TICKS(10)); // queue full: wait rather than drop, this is a test
            }
        }
        if (reader.overruns) {
            // Said out loud: a recording that starts mid-word otherwise reads as a microphone fault,
            // which is the thing this tool exists to rule out.
            ESP_LOGW(TAG, "held longer than the buffer — the start of it is gone");
            reader.overruns = 0;
        }

        // Without the zeros the codec holds its last sample as a tone until something else is
        // written.
        audio_out_silence(120);
        vTaskDelay(pdMS_TO_TICKS(total * 1000 / 16000 + 600));
        audio_out_amp(false);
        replaying = false;
        ESP_LOGI(TAG, "replay done");
    }
}

esp_err_t bench_start(void)
{
    ESP_RETURN_ON_ERROR(state_subscribe(on_sat), TAG, "subscribe");
    if (xTaskCreate(replay_task, "bench", 4 * 1024, NULL, 4, NULL) != pdPASS) {
        return ESP_ERR_NO_MEM;
    }
    return ESP_OK;
}
