#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "esp_err.h"
#include "esp_io_expander_tca95xx_16bit.h"

#ifdef __cplusplus
extern "C" {
#endif

// The board's TCA9555 I/O expander: the speaker amplifier and the buttons hang off it, not off
// GPIOs. A thin wrapper over espressif/esp_io_expander_tca95xx_16bit that owns the handle and the
// board's pin directions; every call reports failure instead of printing and carrying on.

// tca9555_driver_init creates the expander on the board's I2C bus and configures pin directions.
// The bus must already exist. Call once, before anything touches an EXIO pin.
esp_err_t tca9555_driver_init(void);

// tca9555_set_exio drives the masked output pins to state, leaving all others untouched.
esp_err_t tca9555_set_exio(uint32_t pin_mask, uint8_t state);

// tca9555_read_exio reads the masked input pins; *out_state is true if at least one is high.
esp_err_t tca9555_read_exio(uint32_t pin_mask, bool *out_state);

#ifdef __cplusplus
}
#endif
