#include "audio_out.h"

#include "freertos/FreeRTOS.h"
#include "freertos/ringbuf.h"
#include "freertos/task.h"

#include "bsp_board.h"
#include "tca9555_driver.h"
#include "esp_heap_caps.h"
#include "esp_log.h"

static const char *TAG = "sat/out";

// A second of mono 16 kHz PCM16, in PSRAM.
//
// It was three, to absorb the bursts a live model produces — a sentence arrives far faster than it
// is spoken. The daemon meters those out at speaking rate now, so what reaches here is a steady
// stream and this only has to cover network jitter.
//
// Shallow on purpose. Audio queued ahead on the device is audio that cannot be taken back: interrupt
// it and the speaker keeps talking for however much is buffered, and whatever reached the codec's
// DMA is unstoppable. The backlog belongs where it can be discarded in one go.
#define RING_BYTES (32 * 1024)

// One fetch chunk is well under this; the drain task reads whatever has accumulated.
#define DRAIN_BYTES 2048

// How much has to pile up before playback starts: about 150 ms, enough for network jitter now that
// the daemon no longer delivers in bursts.
//
// Audio arrives from the network in bursts and leaves at a codec's steady rate. Playing the instant
// the first chunk lands means running dry between bursts, which is heard as the last block repeating
// — a stutter, not a gap. Waiting for a cushion first is what a browser's audio path does for the
// same reason, and the cost is a fifth of a second before the first word.
#define PREBUFFER_BYTES 4800

// How many silence blocks in a row mean the reply has ended rather than stalled. Each is 64 ms, so
// this is a little under a second.
#define STARVED_LIMIT 12

static RingbufHandle_t ring;

// Written by the drain task, read by whoever reports. A silent speaker has several possible causes
// — nothing queued, nothing drained, or the codec refusing the write — and they are only
// distinguishable by counting each stage separately.
static volatile uint32_t played_chunks;
static volatile uint32_t played_samples;
static volatile int last_write_err;
// How often playback ran dry. The number that says whether the cushion is big enough.
static volatile uint32_t underruns;

// drain_task moves queued samples into the codec. It is the only caller of esp_audio_play, which
// is what keeps that call off the fetch loop.
static void drain_task(void *arg)
{
    // Mono all the way through. The board carries an ES8311, which has a single DAC channel, so a
    // duplicated stereo stream is twice the material for the same amount of time.
    //
    // Once playback starts this loop NEVER stops writing until the reply is over. That is the whole
    // trick: the codec repeats its last block whenever nothing new arrives, so a gap in the writes is
    // heard as a skipping record rather than as a pause. Speech from a live model arrives in bursts,
    // so gaps are certain — they simply have to be filled with silence rather than left empty.
    static const int16_t quiet[DRAIN_BYTES / sizeof(int16_t)] = {0};
    bool playing = false;
    int starved = 0;

    for (;;) {
        size_t waiting = RING_BYTES - xRingbufferGetCurFreeSize(ring);
        if (!playing) {
            // Hold until there is a cushion; starting on the first chunk guarantees running dry.
            if (waiting < PREBUFFER_BYTES) {
                vTaskDelay(pdMS_TO_TICKS(10));
                continue;
            }
            playing = true;
            starved = 0;
        }

        size_t got = 0;
        int16_t *mono = xRingbufferReceiveUpTo(ring, &got, 0, DRAIN_BYTES);
        if (mono) {
            starved = 0;
            esp_err_t err = esp_audio_mono16(mono, got / sizeof(int16_t), portMAX_DELAY);
            if (err != ESP_OK) {
                last_write_err = err;
            }
            played_chunks++;
            played_samples += got / sizeof(int16_t);
            vRingbufferReturnItem(ring, mono);
            continue;
        }

        // Nothing queued. Keep the codec fed so it has no last block to repeat.
        esp_audio_mono16(quiet, sizeof(quiet) / sizeof(quiet[0]), portMAX_DELAY);
        underruns++;
        // After a stretch of silence the reply is over rather than merely stalled, and holding the
        // pipeline open past that only delays the next prebuffer.
        if (++starved > STARVED_LIMIT) {
            playing = false;
        }
    }
}

esp_err_t audio_out_init(void)
{
    // A byte-buffer ring, so a writer's chunk size and the drain size need not agree.
    // PSRAM: three seconds of audio does not belong in the internal heap, which the WiFi stack and
    // the audio front end are already competing for.
    ring = xRingbufferCreateWithCaps(RING_BYTES, RINGBUF_TYPE_BYTEBUF, MALLOC_CAP_SPIRAM);
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

void audio_out_stats(uint32_t *chunks, uint32_t *samples, int *err, uint32_t *dry)
{
    *chunks = played_chunks;
    *samples = played_samples;
    *err = last_write_err;
    *dry = underruns;
    played_chunks = 0;
    played_samples = 0;
    last_write_err = 0;
    underruns = 0;
}
