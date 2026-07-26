#pragma once

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include "led_strip.h"

#ifdef __cplusplus
extern "C" {
#endif


typedef enum {
    RGB_MODE_IDLE = 1,
    RGB_MODE_PLAYING,
    RGB_MODE_REC_COMMAND, 
} RGB_example_mode_t;

typedef enum {
    RGB_COLOR_RED = 1,
    RGB_COLOR_BLUE,
    RGB_COLOR_GREEN, 
    RGB_COLOR_WHITE,
    RGB_COLOR_MAX
} RGB_example_color_t;


led_strip_handle_t configure_led(void);
void RGB_Example(void);
void set_rgb_mode(RGB_example_mode_t mode);
void set_rgb_color(RGB_example_color_t color);

#ifdef __cplusplus
}
#endif
