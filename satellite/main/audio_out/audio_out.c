#include "audio_out.h"

#include "freertos/FreeRTOS.h"
#include "freertos/ringbuf.h"
#include "freertos/task.h"

#include "bsp_board.h"
#include "rgb_led_driver.h"
#include "tca9555_driver.h"
#include "esp_heap_caps.h"
#include "esp_log.h"

static const char *TAG = "sat/out";

// Two seconds of mono 16 kHz PCM16, and this number IS the credit window.
//
// It is sized against the credit loop's round trip, not against jitter, and the ratio is the whole
// point. An earlier attempt used 256 ms against a measured 208 ms round trip — the window and the
// delay in reopening it were the same number, so the queue was empty every time credit came back and
// no amount of tuning either number helped. Two seconds against the same 200 ms is ten to one, and a
// loop with that much slack cannot oscillate.
//
// Depth costs nothing here. Capacity is not latency — what delays the first word is the fill level
// at which playback starts, below — and it costs no barge-in reach either, because a barge-in is
// decided on this board and audio_out_flush empties this ring locally and instantly, however full it
// is.
#define RING_BYTES 64000

// How much has to pile up before playback starts: 200 ms.
//
// This, and only this, is what the listener pays for buffering. Audio arrives from the network in
// bursts and leaves at the codec's steady rate; starting on the first chunk guarantees running dry
// between bursts, which is heard as a stutter rather than a pause. 40 ms was tried and sat below one
// WiFi round trip, so the queue was empty at every refill and two thirds of what the speaker emitted
// was silence the board invented.
#define PREBUFFER_BYTES 6400

// How long the wait for that cushion may last before playing whatever is there anyway.
//
// The cushion is a target, not a condition: a reply's last fragment is whatever is left over, often
// less than the cushion, and waiting for one that will never arrive leaves it in the queue until the
// next reply pushes it out. Measured at 134 ms sitting there for as long as the board stayed up.
#define PREBUFFER_WAIT_TICKS 15

// One fetch chunk is well under this; the drain task reads whatever has accumulated.
#define DRAIN_BYTES 640

// What counts as a fully lit ring, as a mean absolute sample value.
//
// Mean absolute and not peak: a single clipped sample says nothing about how loud a syllable was,
// and peak-driven lighting sits at full brightness through an entire sentence. 6000 of 32767 is
// roughly a loud vowel on this codec, so ordinary speech uses most of the range and a shout merely
// saturates it.
#define LEVEL_FULL_SCALE 6000

// Only every fourth sample is measured. At 16 kHz that is still 80 samples per 20 ms chunk, which
// is far more than an envelope needs, and it keeps this off the core's budget entirely.
#define LEVEL_STRIDE 4

static RingbufHandle_t ring;

// Written by the drain task, read by whoever reports. A silent speaker has several possible causes
// — nothing queued, nothing drained, or the codec refusing the write — and they are only
// distinguishable by counting each stage separately.
static volatile uint32_t played_chunks;
static volatile uint32_t played_samples;
static volatile int last_write_err;

// Bytes that have left the queue and not yet been credited back to the sender. This is the entire
// flow control: the sender may have RING_BYTES outstanding, and every byte played here earns it one
// more. Overflow is structurally impossible rather than merely unlikely.
static volatile size_t freed;

// Set by a flush. After a barge-in the queue is empty by definition, so playback waits for a cushion
// again rather than starting on the first chunk of whatever comes next.
static volatile bool refill;

// report_level hands the ring the loudness of one chunk. Integer throughout: this runs per 20 ms
// chunk on the core the network is on, and an envelope does not need a divide it cannot afford.
static void report_level(const int16_t *mono, size_t samples)
{
    if (samples == 0) {
        return;
    }
    uint32_t sum = 0;
    size_t counted = 0;
    for (size_t i = 0; i < samples; i += LEVEL_STRIDE) {
        int32_t v = mono[i];
        sum += v < 0 ? -v : v;
        counted++;
    }
    uint32_t mean = sum / counted;
    uint32_t scaled = mean * 255 / LEVEL_FULL_SCALE;
    rgb_level(scaled > 255 ? 255 : (uint8_t)scaled);
}

// drain_task moves queued samples into the codec. It is the only caller of esp_audio_play, which
// is what keeps that call off the fetch loop.
static void drain_task(void *arg)
{
    // Mono all the way through. The board carries an ES8311, which has a single DAC channel, so a
    // duplicated stereo stream is twice the material for the same amount of time — it plays at
    // double speed with every sample heard twice. The codec is opened as one channel to match.
    // Once playback starts this loop never stops writing until the reply is over, and the write is
    // what paces it: esp_codec_dev_write blocks until the DMA accepts, so this loop is clocked by the
    // crystal. Stop writing and it stops being clocked — it would spin and burn the core the network
    // is on. Silence is not filler, it is what keeps the loop a clock while nothing is queued.
    static const int16_t quiet[DRAIN_BYTES / sizeof(int16_t)] = {0};
    bool playing = false;
    int held = 0;

    for (;;) {
        size_t waiting = RING_BYTES - xRingbufferGetCurFreeSize(ring);
        if (refill) {
            refill = false;
            playing = false;
        }
        if (!playing) {
            if (waiting < PREBUFFER_BYTES && held < PREBUFFER_WAIT_TICKS) {
                held++;
                vTaskDelay(pdMS_TO_TICKS(10));
                continue;
            }
            if (waiting == 0) {
                held = 0;
                vTaskDelay(pdMS_TO_TICKS(10));
                continue;
            }
            held = 0;
            playing = true;
        }

        size_t got = 0;
        int16_t *mono = xRingbufferReceiveUpTo(ring, &got, 0, DRAIN_BYTES);
        if (!mono) {
            // Nothing queued. Keep the codec fed, which is what keeps this loop clocked.
            esp_audio_mono16(quiet, sizeof(quiet) / sizeof(quiet[0]), portMAX_DELAY);
            playing = false;
            continue;
        }
        // How loud this chunk is, straight to the ring. Measured HERE because this is the last place
        // the samples exist as numbers, and because it is what is about to be heard rather than what
        // was queued a second ago: the ring moves with the words instead of beside them.
        //
        // It leads the speaker by the codec's own DMA buffer, tens of milliseconds. Under the ~200 ms
        // that reads as immediate, and in the right direction — light arriving fractionally before
        // its sound reads as the source of it. The other way round reads as lag.
        report_level(mono, got / sizeof(int16_t));

        esp_err_t err = esp_audio_mono16(mono, got / sizeof(int16_t), portMAX_DELAY);
        if (err != ESP_OK) {
            last_write_err = err;
        }
        played_chunks++;
        played_samples += got / sizeof(int16_t);
        vRingbufferReturnItem(ring, mono);
        // Earned only once the samples have actually gone. Crediting on receipt would hand the
        // sender room the queue does not have yet.
        freed += got;
    }
}

esp_err_t audio_out_init(void)
{
    // A byte-buffer ring, so a writer's chunk size and the drain size need not agree.
    // PSRAM: two seconds does not belong in the internal heap, which the WiFi stack and the audio
    // front end already compete for. Nothing reads it by DMA — the drain task copies each block into
    // an internal scratch on its way to the codec — so the usual PSRAM-and-DMA trap does not apply.
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
    //
    // The switching is not free — see audio_out_amp — but leaving it up is worse: an amplifier held
    // on hisses audibly into a quiet room, and this device sits in one all day. Tried and reverted
    // on that basis.
    Set_EXIO(IO_EXPANDER_PIN_NUM_8, false);

    ESP_LOGI(TAG, "playback queue up (%d bytes)", RING_BYTES);
    return ESP_OK;
}

// audio_out_amp raises or drops the speaker amplifier.
//
// Raising it CLICKS, and the microphone hears the click. That click is invisible to echo
// cancellation, which is the part worth remembering: the canceller subtracts the playback signal it
// is handed digitally, and the click happens AFTER the DAC, in the amplifier itself. Digitally there
// is silence at that moment, so there is nothing to subtract. No amount of NLP, BSS or detector
// tuning reaches it — those all work on what the canceller leaves behind, and here it never had the
// signal to begin with.
//
// Measured: exactly one spurious voice detection per playback, at the start, independent of how long
// the playback ran. A five second replay triggered as often as a three second one, and the
// microphone was silent in between. One switch-on, one false trigger.
//
// It is therefore NOT the audio path's job to suppress: whoever raises the amplifier knows it just
// did, and discards voice activity for a moment afterwards. Tuning the detector for this would blunt
// it for real speech as well, and holding the amplifier up to avoid the click trades it for an
// audible hiss in a quiet room.
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
            break; // break, not return — what follows is the point of calling this
        }
        vRingbufferReturnItem(ring, item);
        // Discarded is just as FREE as played, and the sender has to be told so.
        //
        // Crediting only what reached the speaker means every flush permanently shrinks the window:
        // the sender goes on believing this queue holds bytes that were thrown away, and after a
        // couple of barge-ins it has no room left to send into and the conversation goes quiet.
        // Measured — the second turn got no answer at all.
        freed += got;
    }
    refill = true;
}

size_t audio_out_capacity(void) { return RING_BYTES; }

size_t audio_out_take_freed(void)
{
    size_t n = freed;
    freed -= n;
    return n;
}

size_t audio_out_depth(void) { return ring ? RING_BYTES - xRingbufferGetCurFreeSize(ring) : 0; }

void audio_out_stats(uint32_t *chunks, uint32_t *samples, int *err)
{
    *chunks = played_chunks;
    *samples = played_samples;
    *err = last_write_err;
    played_chunks = 0;
    played_samples = 0;
    last_write_err = 0;
}
