#pragma once

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// The board's three buttons, on the I/O expander rather than on GPIOs.
//
// Only one is used, and only as a bench tool: it starts and stops a capture-and-replay, so the
// microphone path can be heard at any moment without saying the wake word. That matters whenever the
// front end is being changed — gain, echo cancellation, noise suppression — because the only honest
// test of those is listening to what comes out.
//
// Reported as SAT_EV_BUTTON. Which button is in the event's data, so this module decides nothing.

typedef enum {
    BUTTON_A = 9, // I/O expander pin numbers, and the only names the hardware gives them
    BUTTON_B = 10,
    BUTTON_C = 11,
} button_id_t;

// button_start begins scanning. The expander must already be initialised.
esp_err_t button_start(void);

#ifdef __cplusplus
}
#endif
