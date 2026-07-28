#pragma once

#include <stddef.h>
#include <stdint.h>

#include "esp_err.h"
#include "freertos/FreeRTOS.h"

#ifdef __cplusplus
extern "C" {
#endif

// micbuf is the microphone's recent past: one writer appends every frame, any number of readers take
// what they want at their own pace, each with a cursor of its own.
//
// Pull, not push. A consumer running on the front end's fetch loop would stall it, and a stalled
// fetch loop costs the echo canceller its alignment. A single "is capturing" switch would be shared
// by unrelated consumers, and one of them turning it off silences the others.
//
// The history is what makes a wake word usable: the word is already spoken by the time the detector
// says so, and a reader that attaches a moment back still has it.

// micbuf_reader_t is one consumer's position. Zero-initialise, then micbuf_attach. Owned by the one
// task that reads with it; nothing here is shared between readers.
typedef struct {
    uint32_t cursor;   // in samples, modular
    uint32_t overruns; // times the writer lapped this reader
} micbuf_reader_t;

// micbuf_init allocates the buffer. Call once, before the front end starts.
esp_err_t micbuf_init(void);

// micbuf_write appends one frame; the fetch loop is the only caller. It never waits on a reader —
// the oldest samples are overwritten and the reader learns from its own overrun count.
void micbuf_write(const int16_t *pcm, size_t samples);

// micbuf_attach positions a reader back_samples before the present, clamped to what is still held.
void micbuf_attach(micbuf_reader_t *r, uint32_t back_samples);

// micbuf_read copies up to max samples into out and advances the reader, returning 0 if nothing new
// arrived within wait ticks.
//
// It hands out no pointer into the buffer, so the lock cannot be held across a consumer's own work.
// The wake-up is best effort; the timeout is the guarantee.
size_t micbuf_read(micbuf_reader_t *r, int16_t *out, size_t max, TickType_t wait);

// micbuf_written is the total samples ever written, modular. For a reader that wants a fixed stretch
// of the past rather than a live stream.
uint32_t micbuf_written(void);

// micbuf_take_peak reports and clears the loudest sample written since the last call. Measured by
// the writer, so it is valid whether or not anyone is reading.
int32_t micbuf_take_peak(void);

#ifdef __cplusplus
}
#endif
