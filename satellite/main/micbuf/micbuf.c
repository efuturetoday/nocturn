#include "micbuf.h"

#include <string.h>

#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/semphr.h"

#include "esp_heap_caps.h"
#include "esp_check.h"
#include "esp_log.h"

static const char *TAG = "sat/micbuf";

// Two seconds of 16 kHz mono PCM16 in PSRAM, 64 kB. Enough for a wake word's head start and a short
// bench recording; not enough to become somewhere things get stored.
#define CAPACITY (2 * 16000)

// Data lock, held for exactly one memcpy.
static SemaphoreHandle_t lock;

// Broadcast wake-up, so readers need not poll.
static EventGroupHandle_t fresh;
#define BIT_FRESH BIT0

static int16_t *ring;

// Modular: wraps after ~74 hours at 16 kHz, and every use below is a difference between two of them,
// which stays correct across the wrap.
static uint32_t written;

static int32_t peak;

esp_err_t micbuf_init(void)
{
    // PSRAM, and safe there: every path in and out is a memcpy, never DMA.
    ring = heap_caps_malloc(CAPACITY * sizeof(int16_t), MALLOC_CAP_SPIRAM);
    lock = xSemaphoreCreateMutex();
    fresh = xEventGroupCreate();
    ESP_RETURN_ON_FALSE(ring && lock && fresh, ESP_ERR_NO_MEM, TAG, "Failed to allocate history");
    ESP_LOGI(TAG, "microphone history up (%d ms)", CAPACITY * 1000 / 16000);
    return ESP_OK;
}

void micbuf_write(const int16_t *pcm, size_t samples)
{
    if (!ring || samples == 0) {
        return;
    }
    if (samples > CAPACITY) {
        pcm += samples - CAPACITY; // the wrap arithmetic below assumes it
        samples = CAPACITY;
    }

    // Before the lock, out of the caller's own buffer.
    int32_t p = 0;
    for (size_t i = 0; i < samples; i++) {
        int32_t v = pcm[i] < 0 ? -pcm[i] : pcm[i];
        if (v > p) {
            p = v;
        }
    }

    xSemaphoreTake(lock, portMAX_DELAY);
    size_t at = written % CAPACITY;
    size_t first = CAPACITY - at;
    if (first > samples) {
        first = samples;
    }
    memcpy(&ring[at], pcm, first * sizeof(int16_t));
    if (samples > first) {
        memcpy(ring, pcm + first, (samples - first) * sizeof(int16_t));
    }
    written += samples;
    if (p > peak) {
        peak = p;
    }
    xSemaphoreGive(lock);

    xEventGroupSetBits(fresh, BIT_FRESH);
}

void micbuf_attach(micbuf_reader_t *r, uint32_t back_samples)
{
    if (!ring) {
        return;
    }
    if (back_samples > CAPACITY) {
        back_samples = CAPACITY;
    }
    xSemaphoreTake(lock, portMAX_DELAY);
    r->cursor = written - back_samples;
    xSemaphoreGive(lock);
    r->overruns = 0;
}

size_t micbuf_read(micbuf_reader_t *r, int16_t *out, size_t max, TickType_t wait)
{
    if (!ring || max == 0) {
        return 0;
    }
    for (;;) {
        xSemaphoreTake(lock, portMAX_DELAY);
        uint32_t lag = written - r->cursor;
        if (lag > CAPACITY) {
            // Lapped. Resume at the oldest sample still held, not the newest: a stall costs a gap in
            // the middle rather than everything up to now.
            r->cursor = written - CAPACITY;
            lag = CAPACITY;
            r->overruns++;
        }
        if (lag > 0) {
            size_t n = lag < max ? lag : max;
            size_t at = r->cursor % CAPACITY;
            size_t first = CAPACITY - at;
            if (first > n) {
                first = n;
            }
            memcpy(out, &ring[at], first * sizeof(int16_t));
            if (n > first) {
                memcpy(out + first, ring, (n - first) * sizeof(int16_t));
            }
            r->cursor += n;
            xSemaphoreGive(lock);
            return n;
        }
        xSemaphoreGive(lock);

        if (wait == 0) {
            return 0;
        }
        xEventGroupWaitBits(fresh, BIT_FRESH, pdTRUE, pdFALSE, wait);
        wait = 0; // one wait per call; looping is the caller's
    }
}

uint32_t micbuf_written(void)
{
    xSemaphoreTake(lock, portMAX_DELAY);
    uint32_t n = written;
    xSemaphoreGive(lock);
    return n;
}

int32_t micbuf_take_peak(void)
{
    xSemaphoreTake(lock, portMAX_DELAY);
    int32_t p = peak;
    peak = 0;
    xSemaphoreGive(lock);
    return p;
}
