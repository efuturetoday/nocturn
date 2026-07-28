#include "uplink.h"

#include <assert.h>
#include <stdatomic.h>
#include <string.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "esp_heap_caps.h"
#include "esp_check.h"
#include "esp_log.h"
#include "link.h"
#include "micbuf.h"

static const char *TAG = "sat/up";

// 64 ms a frame. Espressif's guidance for realtime audio is 20 to 40 milliseconds, and this
// deliberately exceeds it: at 32 ms the board dropped microphone frames because it could not get
// them onto the socket fast enough, and each frame costs a WebSocket header and a TCP write
// regardless of size. Half as many frames, twice the size, is what the link could actually carry.
#define SEND_SAMPLES 1024

// How far into the past a session begins: 300 ms, covering the syllable already spoken by the time
// the wake word fires. Kept small — everything held goes out at once when the session opens, and a
// larger burst is refused by the link.
#define PRETRIGGER_SAMPLES (16000 / 3)

// How long the sender waits for audio before re-reading open_, and so also the delay before a closed
// uplink notices it was opened.
#define IDLE_WAIT_MS 20

static atomic_bool open_;
static atomic_bool gated;
// Bytes handed to the socket and sends the link refused: a silent far side is otherwise
// indistinguishable from a board that never sent anything.
static atomic_uint_least32_t sent_bytes;
static atomic_uint_least32_t send_fails;
static atomic_uint_least32_t overruns;

// drain_task owns the reader outright — open_ is a request from another task, the cursor is touched
// only here — so neither needs a lock.
static void drain_task(void *arg)
{
    micbuf_reader_t reader = {0};
    int16_t *frame = heap_caps_malloc(SEND_SAMPLES * sizeof(int16_t), MALLOC_CAP_SPIRAM);
    assert(frame);
    bool was_open = false;

    for (;;) {
        bool now = atomic_load(&open_);
        if (now && !was_open) {
            micbuf_attach(&reader, PRETRIGGER_SAMPLES);
        }
        was_open = now;
        if (!now) {
            vTaskDelay(pdMS_TO_TICKS(IDLE_WAIT_MS));
            continue;
        }

        size_t n = micbuf_read(&reader, frame, SEND_SAMPLES, pdMS_TO_TICKS(IDLE_WAIT_MS));
        if (n == 0) {
            continue;
        }
        atomic_store(&overruns, reader.overruns);
        // Silence, not the microphone, while the speaker runs. What the canceller leaves of the
        // board's own voice is louder than a person (measured), and the model's VAD cannot tell the
        // difference — nobody's can. Zeros keep the stream continuous, so the far side sees a clean
        // end of the person's turn instead of a stalled stream.
        if (atomic_load(&gated)) {
            memset(frame, 0, n * sizeof(int16_t));
        }
        // A failed send is not worth retrying: by the time the link is back this audio is stale, and
        // the far side would hear a jump rather than the pause it expects.
        if (link_send_audio((const uint8_t *)frame, n * sizeof(int16_t))) {
            atomic_fetch_add(&sent_bytes, n * sizeof(int16_t));
        } else {
            atomic_fetch_add(&send_fails, 1);
        }
    }
}

esp_err_t uplink_start(void)
{
    // Core 0, away from the audio front end's fetch loop on core 1: the network stack is bursty, and
    // letting it share a core with the loop reintroduces exactly the stall this task prevents.
    ESP_RETURN_ON_FALSE(
        xTaskCreatePinnedToCore(drain_task, "uplink", 4 * 1024, NULL, 4, NULL, 0) == pdPASS,
        ESP_ERR_NO_MEM, TAG, "Failed to create sender task");
    ESP_LOGI(TAG, "uplink up (%d ms frames, %d ms pre-trigger)", SEND_SAMPLES * 1000 / 16000,
             PRETRIGGER_SAMPLES * 1000 / 16000);
    return ESP_OK;
}

void uplink_stats(uint32_t *bytes, uint32_t *fails, uint32_t *late)
{
    *bytes = atomic_exchange(&sent_bytes, 0);
    *fails = atomic_exchange(&send_fails, 0);
    *late = atomic_load(&overruns); // running total, owned by the reader
}

void uplink_open(void) { atomic_store(&open_, true); }

void uplink_gate(bool muted) { atomic_store(&gated, muted); }

// Nothing to discard on close: the next open attaches at a fresh position, so audio from a finished
// conversation is never asked for.
void uplink_close(void) { atomic_store(&open_, false); }
