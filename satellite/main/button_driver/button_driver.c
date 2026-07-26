#include "button_driver.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/timers.h"
#include "esp_timer.h"
#include "tca9555_driver.h"

static const char *TAG = "button_driver";


// 单个按键状态
typedef struct {
    bool is_pressed;         // 当前是否按下（去抖后）
    bool last_pressed;       // 上一次状态
    uint64_t press_start;    // 按下开始时间（ms）
    bool long_triggered;     // 长按是否已触发
} key_state_t;

// 全局变量
static key_state_t s_keys[3] = {0};  // 3个按键
static key_callback_t s_callback = NULL;
static TimerHandle_t s_timer = NULL;
static void *s_user_data = NULL;
static const uint32_t LONG_PRESS_MS = 1000;  // 长按阈值1s


// 从掩码获取按键状态（按下返回true，根据硬件修改）
static bool get_key_state(uint32_t mask, key_id_t id) {
    // 假设：按键按下时对应位为0（低电平），未按下为1（高电平）
    return !(mask & (1ULL << id));
}


// 100ms扫描一次
static void scan_timer_ccan(void *arg) 
{

    while(1)
    {
        uint32_t mask;
        // 一次读取所有关注的引脚（仅一次I2C操作）
        esp_err_t ret = esp_io_expander_get_level(
            io_expander,
            (1ULL << KEY_ID_9) | (1ULL << KEY_ID_10) | (1ULL << KEY_ID_11),
            &mask
        );
        if (ret != ESP_OK) {
            ESP_LOGE(TAG, "读取IO扩展芯片失败");
            return;
        }

        // 处理每个按键
        for (int i = 0; i < 3; i++) {
            key_id_t key_id = KEY_ID_9 + i;
            key_state_t *key = &s_keys[i];

            // 读取当前状态（已去抖，因为扫描周期100ms远大于抖动时间）
            key->is_pressed = get_key_state(mask, key_id);
            uint64_t now = esp_timer_get_time() / 1000;  // 当前时间(ms)

            // 状态变化处理
            if (key->is_pressed && !key->last_pressed) {
                // 刚按下：记录开始时间
                key->press_start = now;
                key->long_triggered = false;
                ESP_LOGD(TAG, "按键%d: 按下", key_id);
            } 
            else if (!key->is_pressed && key->last_pressed) {
                // 刚松开：判断短按
                uint32_t press_time = now - key->press_start;
                if (press_time < LONG_PRESS_MS && !key->long_triggered) {
                    if (s_callback) {
                        s_callback(key_id, KEY_EVENT_SHORT_PRESS, s_user_data);
                    }
                    ESP_LOGD(TAG, "按键%d: 短按（%dms）", key_id, press_time);
                }
                ESP_LOGD(TAG, "按键%d: 松开", key_id);
            } 
            else if (key->is_pressed && !key->long_triggered) {
                // 持续按下：判断长按（仅触发一次）
                if (now - key->press_start >= LONG_PRESS_MS) {
                    if (s_callback) {
                        s_callback(key_id, KEY_EVENT_LONG_PRESS, s_user_data);
                    }
                    key->long_triggered = true;  // 标记已触发
                    ESP_LOGD(TAG, "按键%d: 长按", key_id);
                }
            }

            // 更新上一次状态
            key->last_pressed = key->is_pressed;
        }
        vTaskDelay(pdMS_TO_TICKS(100));
    }
}

// 初始化
esp_err_t key_module_init(void *user_data) {
    s_user_data = user_data;

    // 初始化按键状态
    for (int i = 0; i < 3; i++) {
        s_keys[i].is_pressed = false;
        s_keys[i].last_pressed = false;
        s_keys[i].press_start = 0;
        s_keys[i].long_triggered = false;
    }

    xTaskCreatePinnedToCore(scan_timer_ccan, "RGB Demo",4096, NULL, 3, NULL, 0);
    return ESP_OK;
}

// 注册回调
esp_err_t key_register_callback(key_callback_t callback) {
    if (!callback) return ESP_ERR_INVALID_ARG;
    s_callback = callback;
    return ESP_OK;
}

// 反初始化
esp_err_t key_module_deinit(void) {
    if (s_timer) {
        xTimerStop(s_timer, 0);
        xTimerDelete(s_timer, 0);
    }
    s_callback = NULL;
    return ESP_OK;
}