#include "button.h"

#include "esp_check.h"
#include "esp_log.h"
#include "iot_button.h"

#include "state.h"
#include "tca9555_driver.h"

static const char *TAG = "sat/btn";

// The buttons hang off the I2C expander, not off GPIOs, so none of iot_button's ready-made devices
// fit — but its custom driver does: supply a way to read a level and it owns everything above that,
// which is the part worth not writing again. Debouncing, click counting, long-press timing and the
// state machine that separates a single click from the first half of a double are all decisions with
// well-known wrong answers.
//
// The level read is the only thing this file knows how to do, and it is four lines.
typedef struct {
    button_driver_t base; // first member: __containerof recovers the object from the base pointer
    button_id_t id;
} expander_button_t;

static uint8_t read_level(button_driver_t *driver)
{
    expander_button_t *btn = __containerof(driver, expander_button_t, base);
    // Active low — pressing pulls the pin down, which is how the expander's inputs are wired. The
    // component wants 1 for "pressed", so the inversion belongs here rather than in its logic.
    // A failed read reports released: inventing a press would start a recording nobody asked for.
    bool high = true;
    if (tca9555_read_exio(1UL << btn->id, &high) != ESP_OK) {
        return 0;
    }
    return high ? 0 : 1;
}

static void on_down(void *handle, void *user)
{
    button_id_t id = (button_id_t)(intptr_t)user;
    state_post(SAT_EV_BUTTON_DOWN, &id, sizeof(id));
}

static void on_up(void *handle, void *user)
{
    button_id_t id = (button_id_t)(intptr_t)user;
    state_post(SAT_EV_BUTTON_UP, &id, sizeof(id));
}

static esp_err_t add(button_id_t id)
{
    expander_button_t *btn = calloc(1, sizeof(expander_button_t));
    if (!btn) {
        return ESP_ERR_NO_MEM;
    }
    btn->id = id;
    btn->base.get_key_level = read_level;

    // Zero means the component's defaults, which are the ones its own documentation is written
    // against. Nothing here has a reason to disagree with them.
    button_config_t cfg = {0};
    button_handle_t handle = NULL;
    esp_err_t err = iot_button_create(&cfg, &btn->base, &handle);
    if (err != ESP_OK) {
        free(btn);
        return err;
    }
    // Down and up, not click. Holding to record is the whole interaction: the button IS the state,
    // so there is nothing to remember and nothing that can be out of step with what the device
    // thinks. A toggle needs the person to know which half of it they are in, and this device has no
    // way to tell them.
    ESP_RETURN_ON_ERROR(iot_button_register_cb(handle, BUTTON_PRESS_DOWN, NULL, on_down, (void *)(intptr_t)id),
                        TAG, "down cb");
    return iot_button_register_cb(handle, BUTTON_PRESS_UP, NULL, on_up, (void *)(intptr_t)id);
}

esp_err_t button_start(void)
{
    static const button_id_t ids[] = {BUTTON_A, BUTTON_B, BUTTON_C};
    for (int i = 0; i < (int)(sizeof(ids) / sizeof(ids[0])); i++) {
        ESP_RETURN_ON_ERROR(add(ids[i]), TAG, "button %d", (int)ids[i]);
    }
    ESP_LOGI(TAG, "buttons up");
    return ESP_OK;
}
