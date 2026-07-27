#include "uplink.h"

#include "freertos/FreeRTOS.h"
#include "freertos/ringbuf.h"
#include "freertos/task.h"

#include "esp_heap_caps.h"
#include "esp_log.h"
#include "link.h"

static const char *TAG = "sat/up";

// About a second of speech. Deeper only delays the moment a congested link is noticed, and audio
// that old is worth less than the knowledge that the link cannot keep up.
#define RING_BYTES (32 * 1024)

// 64 ms a frame. Espressif's guidance for realtime audio is 20 to 40 milliseconds, and this
// deliberately exceeds it: at 32 ms the board dropped microphone frames because it could not get
// them onto the socket fast enough, and each frame costs a WebSocket header and a TCP write
// regardless of size. Half as many frames, twice the size, is what the link could actually carry.
#define SEND_BYTES 2048

static RingbufHandle_t ring;
static volatile bool open_;

static void drain_task(void *arg)
{
    for (;;) {
        size_t got = 0;
        void *pcm = xRingbufferReceiveUpTo(ring, &got, portMAX_DELAY, SEND_BYTES);
        if (!pcm) {
            continue;
        }
        // A failed send is not worth retrying: by the time the link is back this audio is stale, and
        // the far side would hear a jump rather than the pause it expects.
        link_send_audio(pcm, got);
        vRingbufferReturnItem(ring, pcm);
    }
}

esp_err_t uplink_start(void)
{
    ring = xRingbufferCreateWithCaps(RING_BYTES, RINGBUF_TYPE_BYTEBUF, MALLOC_CAP_SPIRAM);
    if (!ring) {
        return ESP_ERR_NO_MEM;
    }
    // Core 0, away from the audio front end's fetch loop on core 1: the network stack is bursty, and
    // letting it share a core with the loop reintroduces exactly the stall this queue prevents.
    if (xTaskCreatePinnedToCore(drain_task, "uplink", 4 * 1024, NULL, 4, NULL, 0) != pdPASS) {
        return ESP_ERR_NO_MEM;
    }
    ESP_LOGI(TAG, "uplink queue up (%d bytes)", RING_BYTES);
    return ESP_OK;
}

esp_err_t uplink_write(const int16_t *pcm, size_t samples)
{
    if (!ring || !open_ || samples == 0) {
        return ESP_OK; // nothing listening: not an error, and not a drop worth counting
    }
    if (xRingbufferSend(ring, pcm, samples * sizeof(int16_t), 0) != pdTRUE) {
        return ESP_ERR_NO_MEM;
    }
    return ESP_OK;
}

void uplink_open(void) { open_ = true; }

void uplink_close(void)
{
    open_ = false;
    // Drop whatever is still queued. It belongs to a conversation that is over, and sending it into
    // the next one would put the tail of one question in front of another.
    for (;;) {
        size_t got = 0;
        void *item = xRingbufferReceiveUpTo(ring, &got, 0, SEND_BYTES);
        if (!item) {
            return;
        }
        vRingbufferReturnItem(ring, item);
    }
}
