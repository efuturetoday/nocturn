#include "bsp_board.h"

#include <stdbool.h>
#include <stdlib.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "esp_check.h"
#include "esp_err.h"
#include "esp_log.h"

#include "tca9555_driver.h"

#define ADC_I2S_CHANNEL 4

static const char *TAG = "board";

static i2s_chan_handle_t tx_handle = NULL;
static i2s_chan_handle_t rx_handle = NULL;

static const audio_codec_data_if_t *record_data_if  = NULL;
static const audio_codec_ctrl_if_t *record_ctrl_if  = NULL;
static const audio_codec_if_t      *record_codec_if = NULL;
static esp_codec_dev_handle_t       record_dev      = NULL;

static const audio_codec_data_if_t *play_data_if  = NULL;
static const audio_codec_ctrl_if_t *play_ctrl_if  = NULL;
static const audio_codec_gpio_if_t *play_gpio_if  = NULL;
static const audio_codec_if_t      *play_codec_if = NULL;
static esp_codec_dev_handle_t       play_dev      = NULL;

static i2c_master_bus_handle_t i2c_bus_handle = NULL;

esp_codec_dev_handle_t esp_ret_play_dev(void)
{
    return play_dev;
}

i2c_master_bus_handle_t esp_ret_i2c_handle(void)
{
    return i2c_bus_handle;
}

static esp_err_t i2c_master_init(void)
{
    if (i2c_bus_handle != NULL) {
        return ESP_OK;
    }

    const i2c_master_bus_config_t bus_config = {
        .i2c_port = I2C_NUM,
        .sda_io_num = GPIO_I2C_SDA,
        .scl_io_num = GPIO_I2C_SCL,
        .clk_source = I2C_CLK_SRC_DEFAULT,
    };

    ESP_RETURN_ON_ERROR(i2c_new_master_bus(&bus_config, &i2c_bus_handle), TAG,
                        "Failed to initialize I2C bus");
    return ESP_OK;
}

static esp_err_t bsp_codec_adc_init(int sample_rate)
{
    ESP_RETURN_ON_FALSE(i2c_bus_handle != NULL, ESP_ERR_INVALID_STATE, TAG,
                        "I2C must be initialized before ADC");

    audio_codec_i2s_cfg_t i2s_cfg = {
        .port = I2S_NUM_1,
        .rx_handle = rx_handle,
        .tx_handle = NULL,
    };
    record_data_if = audio_codec_new_i2s_data(&i2s_cfg);
    ESP_RETURN_ON_FALSE(record_data_if != NULL, ESP_FAIL, TAG, "Failed to create record data IF");

    audio_codec_i2c_cfg_t i2c_cfg = {
        .addr = ES7210_CODEC_DEFAULT_ADDR,
        .bus_handle = i2c_bus_handle,
    };
    record_ctrl_if = audio_codec_new_i2c_ctrl(&i2c_cfg);
    ESP_RETURN_ON_FALSE(record_ctrl_if != NULL, ESP_FAIL, TAG, "Failed to create record ctrl IF");

    es7210_codec_cfg_t es7210_cfg = {
        .ctrl_if = record_ctrl_if,
        .mic_selected = ES7210_SEL_MIC1 | ES7210_SEL_MIC2 | ES7210_SEL_MIC3 | ES7210_SEL_MIC4,
    };
    record_codec_if = es7210_codec_new(&es7210_cfg);
    ESP_RETURN_ON_FALSE(record_codec_if != NULL, ESP_FAIL, TAG, "Failed to create ES7210 codec");

    esp_codec_dev_cfg_t dev_cfg = {
        .codec_if = record_codec_if,
        .data_if = record_data_if,
        .dev_type = ESP_CODEC_DEV_TYPE_IN,
    };
    record_dev = esp_codec_dev_new(&dev_cfg);
    ESP_RETURN_ON_FALSE(record_dev != NULL, ESP_FAIL, TAG, "Failed to create record device");

    esp_codec_dev_sample_info_t fs = {
        .sample_rate = sample_rate,
        .channel = 2,
        .bits_per_sample = 32,
    };
    ESP_RETURN_ON_ERROR(esp_codec_dev_open(record_dev, &fs), TAG, "Failed to open record device");

    esp_codec_dev_set_in_channel_gain(record_dev, ESP_CODEC_DEV_MAKE_CHANNEL_MASK(0), RECORD_VOLUME);
    esp_codec_dev_set_in_channel_gain(record_dev, ESP_CODEC_DEV_MAKE_CHANNEL_MASK(1), RECORD_VOLUME);
    esp_codec_dev_set_in_channel_gain(record_dev, ESP_CODEC_DEV_MAKE_CHANNEL_MASK(2), RECORD_VOLUME);
    esp_codec_dev_set_in_channel_gain(record_dev, ESP_CODEC_DEV_MAKE_CHANNEL_MASK(3), RECORD_VOLUME);

    return ESP_OK;
}

static esp_err_t bsp_codec_dac_init(int sample_rate, int channel_format, int bits_per_chan)
{
    ESP_RETURN_ON_FALSE(i2c_bus_handle != NULL, ESP_ERR_INVALID_STATE, TAG,
                        "I2C must be initialized before DAC");

    audio_codec_i2s_cfg_t i2s_cfg = {
        .port = I2S_NUM_1,
        .rx_handle = NULL,
        .tx_handle = tx_handle,
    };
    play_data_if = audio_codec_new_i2s_data(&i2s_cfg);
    ESP_RETURN_ON_FALSE(play_data_if != NULL, ESP_FAIL, TAG, "Failed to create play data IF");

    audio_codec_i2c_cfg_t i2c_cfg = {
        .addr = ES8311_CODEC_DEFAULT_ADDR,
        .bus_handle = i2c_bus_handle,
    };
    play_ctrl_if = audio_codec_new_i2c_ctrl(&i2c_cfg);
    ESP_RETURN_ON_FALSE(play_ctrl_if != NULL, ESP_FAIL, TAG, "Failed to create play ctrl IF");
    play_gpio_if = audio_codec_new_gpio();
    ESP_RETURN_ON_FALSE(play_gpio_if != NULL, ESP_FAIL, TAG, "Failed to create play gpio IF");

    es8311_codec_cfg_t es8311_cfg = {
        .codec_mode = ESP_CODEC_DEV_WORK_MODE_DAC,
        .ctrl_if = play_ctrl_if,
        .gpio_if = play_gpio_if,
        .pa_pin = GPIO_PWR_CTRL,
        .use_mclk = false,
    };
    play_codec_if = es8311_codec_new(&es8311_cfg);
    ESP_RETURN_ON_FALSE(play_codec_if != NULL, ESP_FAIL, TAG, "Failed to create ES8311 codec");

    esp_codec_dev_cfg_t dev_cfg = {
        .codec_if = play_codec_if,
        .data_if = play_data_if,
        .dev_type = ESP_CODEC_DEV_TYPE_OUT,
    };
    play_dev = esp_codec_dev_new(&dev_cfg);
    ESP_RETURN_ON_FALSE(play_dev != NULL, ESP_FAIL, TAG, "Failed to create play device");

    esp_codec_dev_sample_info_t fs = {
        .bits_per_sample = bits_per_chan,
        .sample_rate = sample_rate,
        .channel = channel_format,
    };
    ESP_RETURN_ON_ERROR(esp_codec_dev_set_out_vol(play_dev, PLAYER_VOLUME), TAG,
                        "Failed to set out volume");
    ESP_RETURN_ON_ERROR(esp_codec_dev_open(play_dev, &fs), TAG, "Failed to open play device");

    return ESP_OK;
}

esp_err_t esp_audio_set_play_vol(int volume)
{
    ESP_RETURN_ON_FALSE(play_dev != NULL, ESP_ERR_INVALID_STATE, TAG, "DAC codec not initialized");
    return esp_codec_dev_set_out_vol(play_dev, volume);
}

esp_err_t esp_audio_get_play_vol(int *volume)
{
    ESP_RETURN_ON_FALSE(play_dev != NULL, ESP_ERR_INVALID_STATE, TAG, "DAC codec not initialized");
    ESP_RETURN_ON_FALSE(volume != NULL, ESP_ERR_INVALID_ARG, TAG, "volume pointer is NULL");
    return esp_codec_dev_get_out_vol(play_dev, volume);
}

static esp_err_t bsp_i2s_init(i2s_port_t i2s_num)
{
    i2s_chan_config_t chan_cfg = I2S_CHANNEL_DEFAULT_CONFIG(i2s_num, I2S_ROLE_MASTER);
    // An underrun must emit silence, not repeat the last descriptor. The default is false, and the
    // symptom is unmistakable once heard: "the speaker re-re-repeats it-it-itself". TX only, though
    // one config creates both channels here.
    chan_cfg.auto_clear = true;

    ESP_RETURN_ON_ERROR(i2s_new_channel(&chan_cfg, &tx_handle, &rx_handle), TAG,
                        "Failed to create I2S channels");

    i2s_std_config_t std_cfg = I2S_CONFIG_DEFAULT();

    ESP_RETURN_ON_ERROR(i2s_channel_init_std_mode(tx_handle, &std_cfg), TAG, "Failed to init TX std mode");
    ESP_RETURN_ON_ERROR(i2s_channel_init_std_mode(rx_handle, &std_cfg), TAG, "Failed to init RX std mode");
    ESP_RETURN_ON_ERROR(i2s_channel_enable(tx_handle), TAG, "Failed to enable TX channel");
    ESP_RETURN_ON_ERROR(i2s_channel_enable(rx_handle), TAG, "Failed to enable RX channel");

    return ESP_OK;
}

// Static conversion scratch, 8 kB of .bss. The drain task calls esp_audio_mono16 fifty times a
// second and never stops, so a per-call malloc is continuous heap churn on the core the network is
// on. Safe as one shared buffer because that task is the only caller.
#define AUDIO_CHUNK_SAMPLES 1024
static int32_t s_audio_conv_buf[AUDIO_CHUNK_SAMPLES * 2];

// The one and only conversion onto the bus. I2S_CONFIG_DEFAULT fixes the hardware format at 16 kHz,
// 32-bit slots, stereo — 8 bytes per frame — whatever anyone asks for, so mono PCM16 must be
// expanded fourfold or it plays at the wrong speed. Both wrong speeds were heard before this was
// traced.
esp_err_t esp_audio_mono16(const int16_t *data, int samples, uint32_t ticks_to_wait)
{
    (void)ticks_to_wait;
    ESP_RETURN_ON_FALSE(play_dev != NULL, ESP_ERR_INVALID_STATE, TAG, "Play dev not initialized");

    int processed = 0;
    while (processed < samples) {
        int chunk = samples - processed > AUDIO_CHUNK_SAMPLES ? AUDIO_CHUNK_SAMPLES : samples - processed;
        for (int i = 0; i < chunk; i++) {
            int32_t v = (int32_t)data[processed + i] << 16; // 16-bit sample in the top half of a slot
            s_audio_conv_buf[2 * i] = v;
            s_audio_conv_buf[2 * i + 1] = v;
        }
        ESP_RETURN_ON_ERROR(
            esp_codec_dev_write(play_dev, s_audio_conv_buf, chunk * 2 * sizeof(int32_t)),
            TAG, "Audio write failed during chunk");
        processed += chunk;
    }

    return ESP_OK;
}

esp_err_t esp_audio_amp(bool on)
{
    esp_err_t ret = tca9555_set_exio(IO_EXPANDER_PIN_NUM_8, on);
    vTaskDelay(pdMS_TO_TICKS(10)); // settle: switching is audible until the rail is stable
    return ret;
}

esp_err_t esp_get_feed_data(bool is_get_raw_channel, int16_t *buffer, int buffer_len)
{
    ESP_RETURN_ON_FALSE(record_dev != NULL, ESP_ERR_INVALID_STATE, TAG, "Record dev not initialized");
    ESP_RETURN_ON_FALSE(buffer != NULL, ESP_ERR_INVALID_ARG, TAG, "Buffer is NULL");

    ESP_RETURN_ON_ERROR(esp_codec_dev_read(record_dev, (void *)buffer, buffer_len), TAG,
                        "Codec read failed");

    if (!is_get_raw_channel) {
        int audio_chunksize = buffer_len / (sizeof(int16_t) * ADC_I2S_CHANNEL);
        for (int i = 0; i < audio_chunksize; i++) {
            int16_t ref = buffer[4 * i + 0];
            buffer[3 * i + 0] = buffer[4 * i + 1];
            buffer[3 * i + 1] = buffer[4 * i + 3];
            buffer[3 * i + 2] = ref;
        }
    }

    return ESP_OK;
}

int esp_get_feed_channel(void)
{
    return ADC_I2S_CHANNEL;
}

char *esp_get_input_format(void)
{
    return "RMNM";
}

esp_err_t esp_board_init(void)
{
    ESP_RETURN_ON_ERROR(i2c_master_init(), TAG, "Failed to init I2C");
    ESP_RETURN_ON_ERROR(bsp_i2s_init(I2S_NUM_1), TAG, "Failed to init I2S");
    ESP_RETURN_ON_ERROR(bsp_codec_adc_init(16000), TAG, "Failed to init ADC codec");
    ESP_RETURN_ON_ERROR(bsp_codec_dac_init(16000, 2, 32), TAG, "Failed to init DAC codec");
    return ESP_OK;
}
