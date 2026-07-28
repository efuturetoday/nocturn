#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "driver/gpio.h"
#include "driver/i2c_master.h"
#include "driver/i2s_std.h"

#include "esp_codec_dev.h"
#include "esp_codec_dev_defaults.h"
#include "esp_err.h"
#include "sdkconfig.h"

#ifdef __cplusplus
extern "C" {
#endif

// The board: ES7210 ADC (four microphones), ES8311 DAC, both on one I2S instance sharing the codec
// clock, an I2C bus underneath, and the speaker amplifier on the I/O expander. Everything the
// hardware fixes — pins, rates, slot format — lives here and nowhere else.

#define I2C_NUM         (0)
#define GPIO_I2C_SCL    (GPIO_NUM_10)
#define GPIO_I2C_SDA    (GPIO_NUM_11)

#define GPIO_I2S_LRCK   (GPIO_NUM_14)
#define GPIO_I2S_MCLK   (GPIO_NUM_12)
#define GPIO_I2S_SCLK   (GPIO_NUM_13)
#define GPIO_I2S_SDIN   (GPIO_NUM_15)
#define GPIO_I2S_DOUT   (GPIO_NUM_16)

// Analog input gain in dB, ES7210 tops out at 37.5. Normal speech across a room peaks near
// -35 dBFS at 30 dB, which no later stage recovers: the AFE's AGC only compresses toward its
// target, it does not lift a weak signal. Raising the level belongs here, before quantisation.
#define RECORD_VOLUME   (36.0)
#define PLAYER_VOLUME   (60)

// No pin: the amplifier is on the I/O expander, driven via esp_audio_amp, not by the codec driver.
#define GPIO_PWR_CTRL       (-1)
#define GPIO_PWR_ON_LEVEL   (1)

// The bus format is fixed: 16 kHz, 32-bit slots, stereo — 8 bytes per frame. Everything feeding the
// bus converts to this; see esp_audio_mono16.
#define I2S_CONFIG_DEFAULT() { \
        .clk_cfg  = I2S_STD_CLK_DEFAULT_CONFIG(16000), \
        .slot_cfg = I2S_STD_PHILIPS_SLOT_DEFAULT_CONFIG(32, I2S_SLOT_MODE_STEREO), \
        .gpio_cfg = { \
            .mclk = GPIO_I2S_MCLK, \
            .bclk = GPIO_I2S_SCLK, \
            .ws   = GPIO_I2S_LRCK, \
            .dout = GPIO_I2S_DOUT, \
            .din  = GPIO_I2S_SDIN, \
        }, \
    }

#define LED_STRIP_GPIO_PIN  38
#define LED_STRIP_LED_COUNT 7

// esp_board_init brings up I2C, I2S and both codecs, in that order. No parameters, because the
// hardware format is not negotiable — it used to take a sample rate and ignore it.
esp_err_t esp_board_init(void);

/**
 * @brief Play mono 16-bit PCM, expanded to the 32-bit stereo frames the I2S bus actually carries.
 *        @p samples is a COUNT, not a byte length.
 */
esp_err_t esp_audio_mono16(const int16_t *data, int samples, uint32_t ticks_to_wait);

esp_err_t esp_get_feed_data(bool is_get_raw_channel, int16_t *buffer, int buffer_len);
int esp_get_feed_channel(void);
char *esp_get_input_format(void);
esp_err_t esp_audio_set_play_vol(int volume);
esp_err_t esp_audio_get_play_vol(int *volume);

/**
 * @brief Raise or drop the speaker amplifier and wait out its settle time. Which pin that is, and
 *        how long it needs, are this board's wiring — no caller should know either.
 */
esp_err_t esp_audio_amp(bool on);

i2c_master_bus_handle_t esp_ret_i2c_handle(void);
esp_codec_dev_handle_t esp_ret_play_dev(void);

#ifdef __cplusplus
}
#endif
