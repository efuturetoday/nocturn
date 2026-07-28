#pragma once

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// bench is hold-to-record, release-to-hear-it-back, on button A. It exists to make a gain or echo
// cancellation change audible without the wake word, which needs a quiet room and is itself one of
// the things under test.
//
// Held rather than toggled: a toggle needs the person to know which half they are in, and seven LEDs
// cannot tell them. Record then play, never both — a live loopback feeds the echo canceller its own
// output, so the better it works the quieter the test gets.
//
// It commands nothing at the microphone; it holds a micbuf reader like any other consumer. The
// speaker is genuinely shared, so it asks state/ once per press whether a conversation is running.

// bench_start registers the button handler and the replay task. Call after state_start and micbuf.
esp_err_t bench_start(void);

#ifdef __cplusplus
}
#endif
