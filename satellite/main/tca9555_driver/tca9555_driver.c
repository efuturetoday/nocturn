#include "tca9555_driver.h"

#include <inttypes.h>

#include "esp_check.h"
#include "esp_log.h"

#include "bsp_board.h"

static const char *TAG = "tca9555";

esp_io_expander_handle_t io_expander = NULL;

esp_err_t tca9555_driver_init(void)
{
    i2c_master_bus_handle_t i2c_bus = esp_ret_i2c_handle();
    ESP_RETURN_ON_FALSE(i2c_bus != NULL, ESP_ERR_INVALID_STATE, TAG, "I2C bus not initialized");

    ESP_RETURN_ON_ERROR(
        esp_io_expander_new_i2c_tca95xx_16bit(i2c_bus, ESP_IO_EXPANDER_I2C_TCA9555_ADDRESS_000, &io_expander),
        TAG, "Failed to create TCA9555 IO expander");

    ESP_RETURN_ON_ERROR(
        esp_io_expander_set_dir(io_expander, (IO_EXPANDER_PIN_NUM_0 | IO_EXPANDER_PIN_NUM_1 | IO_EXPANDER_PIN_NUM_5 | IO_EXPANDER_PIN_NUM_6 | IO_EXPANDER_PIN_NUM_8), IO_EXPANDER_OUTPUT),
        TAG, "Failed to set EXIO output direction");

    ESP_RETURN_ON_ERROR(
        esp_io_expander_set_dir(io_expander, (IO_EXPANDER_PIN_NUM_2 | IO_EXPANDER_PIN_NUM_9 | IO_EXPANDER_PIN_NUM_10 | IO_EXPANDER_PIN_NUM_11), IO_EXPANDER_INPUT),
        TAG, "Failed to set EXIO input direction");

    ESP_LOGI(TAG, "TCA9555 driver initialized");
    return ESP_OK;
}

esp_err_t tca9555_set_exio(uint32_t pin_mask, uint8_t state)
{
    ESP_RETURN_ON_FALSE(io_expander != NULL, ESP_ERR_INVALID_STATE, TAG, "Driver not initialized");

    ESP_RETURN_ON_ERROR(
        esp_io_expander_set_level(io_expander, pin_mask, state),
        TAG, "Failed to set EXIO level for mask 0x%" PRIx32, pin_mask);

    return ESP_OK;
}

esp_err_t tca9555_read_exio(uint32_t pin_mask, bool *out_state)
{
    ESP_RETURN_ON_FALSE(io_expander != NULL, ESP_ERR_INVALID_STATE, TAG, "Driver not initialized");
    ESP_RETURN_ON_FALSE(out_state != NULL, ESP_ERR_INVALID_ARG, TAG, "out_state pointer is NULL");

    uint32_t input_level_mask = 0;
    ESP_RETURN_ON_ERROR(
        esp_io_expander_get_level(io_expander, pin_mask, &input_level_mask),
        TAG, "Failed to read EXIO level for mask 0x%" PRIx32, pin_mask);

    // True if at least one of the requested pins is high.
    *out_state = (input_level_mask & pin_mask) != 0;

    return ESP_OK;
}
