#include "rgb_led_driver.h"

#include <stdio.h>
#include "esp_log.h"
#include "esp_err.h"
#include "bsp_board.h"

static const char *TAG = "rgb_led_driver";

typedef struct {
    uint8_t r;
    uint8_t g;
    uint8_t b;
} rgb_color_t;

// 3. 用定义的结构体类型创建映射数组
static const rgb_color_t color_rgb_map[RGB_COLOR_MAX] = {
    [RGB_COLOR_RED]    = {255, 0,   0},     // 红色
    [RGB_COLOR_BLUE]   = {0,   0,   255},   // 蓝色
    [RGB_COLOR_GREEN]  = {0,   255, 0},     // 绿色
    [RGB_COLOR_WHITE]  = {255, 255, 255},   // 白色
};

typedef enum {
    RGB_CMD_SET_MODE = 1,
    RGB_CMD_SET_COLOR, 
} rgb_queue_cmd_t;

typedef struct {
    rgb_queue_cmd_t cmd;     // 命令
    union {                    // 嵌套联合体：
        RGB_example_mode_t mode;    // 模式
        RGB_example_color_t color;     // 颜色
    } param;
} rgb_queue_data_t;


static led_strip_handle_t led_strip;
static QueueHandle_t g_msg_queue;
static RGB_example_color_t RGB_color = RGB_COLOR_WHITE;

led_strip_handle_t configure_led(void)
{
    // LED strip general initialization, according to your led board design
    led_strip_config_t strip_config = {
        .strip_gpio_num = LED_STRIP_GPIO_PIN, // The GPIO that connected to the LED strip's data line
        .max_leds = LED_STRIP_LED_COUNT,      // The number of LEDs in the strip,
        .led_model = LED_MODEL_WS2812,        // LED strip model
        .color_component_format = LED_STRIP_COLOR_COMPONENT_FMT_RGB, // The color order of the strip: GRB
        .flags = {
            .invert_out = false, // don't invert the output signal
        }
    };

    // LED strip backend configuration: RMT
    led_strip_rmt_config_t rmt_config = {
        .clk_src = RMT_CLK_SRC_DEFAULT,        // different clock source can lead to different power consumption
        .resolution_hz = 10 * 1000 * 1000, // RMT counter clock frequency
        .mem_block_symbols = 0, // the memory block size used by the RMT channel
        .flags = {
            .with_dma = 0,     // Using DMA can improve performance when driving more LEDs
        }
    };

    // LED Strip object handle
    led_strip_handle_t led_strip;
    ESP_ERROR_CHECK(led_strip_new_rmt_device(&strip_config, &rmt_config, &led_strip));
    ESP_LOGI(TAG, "Created LED strip object with RMT backend");
    return led_strip;
}


void _set_single_led_color(RGB_example_color_t color ,uint32_t led_index)
{
    rgb_color_t current_color = color_rgb_map[color];

    /*
    
    */
    ESP_ERROR_CHECK(led_strip_set_pixel(led_strip, led_index, current_color.r, current_color.g, current_color.b));
    /* Refresh the strip to send data */
    ESP_ERROR_CHECK(led_strip_refresh(led_strip));
}

//100ms scan
void _example_playing(bool *reset)
{
    static uint16_t time_count = 0;
    static uint8_t led_num = 0;
    static bool clean_flag = 0;
    if(*reset)
    {
        time_count = 0;
        led_num = 0;
        *reset = 0;
        ESP_ERROR_CHECK(led_strip_clear(led_strip));
    }

    time_count++;
    if(time_count>=5)
    {
        time_count=0;

        if(clean_flag!=1)
        {
            _set_single_led_color(RGB_color,led_num++);
            if(led_num>=LED_STRIP_LED_COUNT)
            {
                led_num=0;
                clean_flag = 1;
            }
        }
        else
        {
            ESP_ERROR_CHECK(led_strip_clear(led_strip));
            clean_flag = 0;
        }
            
    }
}

//100ms scan
void _example_esp_sr_rec()
{
    static uint16_t time_count = 0;
    static bool led_on_off = 0;

    time_count++;
    if(time_count>=2)
    {
        time_count=0;
        if (led_on_off) 
        {
            for (int i = 0; i < LED_STRIP_LED_COUNT; i++) 
            {
                _set_single_led_color(RGB_color,i);
            }
        } 
        else 
        {
            ESP_ERROR_CHECK(led_strip_clear(led_strip));
        }

        led_on_off = !led_on_off;
    }
        

}

void _RGB_Example(void *arg)
{
    static RGB_example_mode_t RGB_mode = RGB_MODE_IDLE;
    rgb_queue_data_t msg;
    bool reset_playing_color = 0;
    for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
        _set_single_led_color(RGB_color,i);
    }
    while (1) 
    {
        BaseType_t ret = xQueueReceive(g_msg_queue, &msg, pdMS_TO_TICKS(100));
        if (ret == pdPASS) 
        {
            switch (msg.cmd)
            {
                case RGB_CMD_SET_MODE:
                    RGB_mode = msg.param.mode;
                    //ESP_LOGI(TAG, "收到消息: 模式=%d", msg.param.mode);
                    ESP_ERROR_CHECK(led_strip_clear(led_strip));
                    if(RGB_mode == RGB_MODE_IDLE)
                    {
                        for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
                            _set_single_led_color(RGB_color,i);
                        }
                    }
                    break;
                case RGB_CMD_SET_COLOR:
                    RGB_color = msg.param.color;   
                    reset_playing_color=1;
                    //ESP_LOGI(TAG, "收到消息: 颜色=%d", msg.param.color);
                    break;
                default:
                    break;
            }
            

        } 
        else 
        {
            switch (RGB_mode)
            {
                case RGB_MODE_IDLE:
                    
                    break;
                case RGB_MODE_PLAYING:
                    _example_playing(&reset_playing_color);
                    break;
                case RGB_MODE_REC_COMMAND:
                    _example_esp_sr_rec();
                    break;
                default:
                    break;
            }
        }
    }
}

void RGB_Example(void)
{
    led_strip = configure_led();
    g_msg_queue = xQueueCreate(3, sizeof(rgb_queue_data_t));
    if (g_msg_queue == NULL) {
        ESP_LOGE(TAG, "队列创建失败");
        return;
    }
    xTaskCreatePinnedToCore(_RGB_Example, "RGB Demo",4096, NULL, 2, NULL, 0);
}


void set_rgb_mode(RGB_example_mode_t mode)
{
    rgb_queue_data_t msg;
    msg.cmd = RGB_CMD_SET_MODE;
    msg.param.mode = mode;
        
    // 发送到队列（超时100ms）
    BaseType_t ret = xQueueSend(g_msg_queue, &msg, pdMS_TO_TICKS(1000));
    if (ret != pdPASS) 
    {
        ESP_LOGE(TAG, "指令发送失败！队列已满");
    }
}

void set_rgb_color(RGB_example_color_t color)
{
    rgb_queue_data_t msg;
    msg.cmd = RGB_CMD_SET_COLOR;
    msg.param.color = color;
        
    // 发送到队列（超时100ms）
    BaseType_t ret = xQueueSend(g_msg_queue, &msg, pdMS_TO_TICKS(1000));
    if (ret != pdPASS) 
    {
        ESP_LOGE(TAG, "指令发送失败！队列已满");
    }
}