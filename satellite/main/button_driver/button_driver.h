#pragma once

#include "freertos/FreeRTOS.h"

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif


// 按键ID（对应扩展芯片引脚）
typedef enum {
    KEY_ID_9 = 9,
    KEY_ID_10 = 10,
    KEY_ID_11 = 11
} key_id_t;

// 按键事件
typedef enum {
    KEY_EVENT_SHORT_PRESS,  // 短按（按下后松开，时间<1s）
    KEY_EVENT_LONG_PRESS    // 长按（按下超过1s）
} key_event_t;

// 回调函数类型
typedef void (*key_callback_t)(key_id_t key_id, key_event_t event, void *user_data);

// 初始化按键模块
esp_err_t key_module_init(void *user_data);

// 注册回调函数
esp_err_t key_register_callback(key_callback_t callback);

// 反初始化
esp_err_t key_module_deinit(void);


#ifdef __cplusplus
}
#endif

