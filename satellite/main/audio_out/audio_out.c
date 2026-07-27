#include "audio_out.h"

#include "freertos/FreeRTOS.h"
#include "freertos/ringbuf.h"
#include "freertos/task.h"

#include "bsp_board.h"
#include "tca9555_driver.h"
#include "esp_heap_caps.h"
#include "esp_log.h"

static const char *TAG = "sat/out";

// Roughly a second of mono 16 kHz PCM16. Deep enough to ride out a codec hiccup, shallow enough
// that a barge-in flush does not leave a noticeable tail.
#define RING_BYTES (32 * 1024)

// One fetch chunk is well under this; the drain task reads whatever has accumulated.
#define DRAIN_BYTES 2048

static RingbufHandle_t ring;

// Written by the drain task, read by whoever reports. A silent speaker has several possible causes
// — nothing queued, nothing drained, or the codec refusing the write — and they are only
// distinguishable by counting each stage separately.
static volatile uint32_t played_chunks;
static volatile uint32_t played_samples;
static volatile int last_write_err;

// drain_task moves queued samples into the codec. It is the only caller of esp_audio_play, which
// is what keeps that call off the fetch loop.
static void drain_task(void *arg)
{
    // Mono all the way through. The board carries an ES8311, which has a single DAC channel, so a
    // duplicated stereo stream is twice the material for the same amount of time — it plays at
    // double speed with every sample heard twice. The codec is opened as one channel to match.
    for (;;) {
        size_t got = 0;
        int16_t *mono = xRingbufferReceiveUpTo(ring, &got, portMAX_DELAY, DRAIN_BYTES);
        if (!mono) {
            continue;
        }
        esp_err_t err = esp_audio_mono16(mono, got / sizeof(int16_t), portMAX_DELAY);
        if (err != ESP_OK) {
            last_write_err = err;
        }
        played_chunks++;
        played_samples += got / sizeof(int16_t);
        vRingbufferReturnItem(ring, mono);
    }
}

esp_err_t audio_out_init(void)
{
    // A byte-buffer ring, so a writer's chunk size and the drain size need not agree.
    ring = xRingbufferCreate(RING_BYTES, RINGBUF_TYPE_BYTEBUF);
    if (!ring) {
        return ESP_ERR_NO_MEM;
    }
    // Pinned opposite the fetch loop: the two must not compete for one core, or the stall this
    // whole module exists to prevent comes back through the scheduler.
    if (xTaskCreatePinnedToCore(drain_task, "audio_out", 4 * 1024, NULL, 5, NULL, 0) != pdPASS) {
        return ESP_ERR_NO_MEM;
    }
    // The speaker amplifier is on the I/O expander, not the codec. The vendor enabled it inside the
    // media player, so removing that left a board that renders audio perfectly into silence. It
    // starts DOWN here and is raised only while something is actually playing.
    Set_EXIO(IO_EXPANDER_PIN_NUM_8, false);

    ESP_LOGI(TAG, "playback queue up (%d bytes)", RING_BYTES);
    return ESP_OK;
}

void audio_out_amp(bool on)
{
    Set_EXIO(IO_EXPANDER_PIN_NUM_8, on);
    vTaskDelay(pdMS_TO_TICKS(10));
}

void audio_out_silence(int ms)
{
    static const int16_t zeros[512] = {0};
    int samples = 16 * ms; // 16 kHz
    while (samples > 0) {
        int take = samples < (int)(sizeof(zeros) / sizeof(zeros[0])) ? samples : (int)(sizeof(zeros) / sizeof(zeros[0]));
        while (audio_out_write(zeros, take) != ESP_OK) {
            vTaskDelay(pdMS_TO_TICKS(5));
        }
        samples -= take;
    }
}

esp_err_t audio_out_write(const int16_t *mono, size_t samples)
{
    if (!ring || !mono || samples == 0) {
        return ESP_ERR_INVALID_ARG;
    }
    // Zero wait: see the header. A drop is a click, a block is a broken echo canceller.
    if (xRingbufferSend(ring, mono, samples * sizeof(int16_t), 0) != pdTRUE) {
        return ESP_ERR_NO_MEM;
    }
    return ESP_OK;
}

void audio_out_flush(void)
{
    if (!ring) {
        return;
    }
    for (;;) {
        size_t got = 0;
        void *item = xRingbufferReceiveUpTo(ring, &got, 0, DRAIN_BYTES);
        if (!item) {
            return;
        }
        vRingbufferReturnItem(ring, item);
    }
}

void audio_out_stats(uint32_t *chunks, uint32_t *samples, int *err)
{
    *chunks = played_chunks;
    *samples = played_samples;
    *err = last_write_err;
    played_chunks = 0;
    played_samples = 0;
    last_write_err = 0;
}
