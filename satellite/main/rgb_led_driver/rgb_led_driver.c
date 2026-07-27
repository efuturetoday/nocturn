#include "rgb_led_driver.h"

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "bsp_board.h"
#include "esp_log.h"
#include "led_strip.h"

static const char *TAG = "sat/rgb";

// How often the renderer redraws. 50 ms is 20 Hz: fast enough that a breath looks continuous and a
// travelling pixel does not stutter, slow enough that the whole thing is a rounding error against
// the audio front end it shares a core with.
#define TICK_MS 50

// One breath, in ticks. Two seconds — the pace of calm breathing, which is the point: it should read
// as waiting, not as loading. Double that when the device is waiting on something other than the
// person, so patience and invitation are distinguishable without looking closely.
#define BREATHE_TICKS 40
#define BREATHE_SLOW_TICKS 80

// Ticks per step of the travelling pixel. 100 ms per LED is a little under a second for the full
// ring, which reads as deliberate rather than frantic.
#define SPIN_TICKS 2

// One full pass of the crest, in ticks. Faster than a breath and slower than a blink, so speech does
// not get confused with either.
#define WAVE_TICKS 24

// Half a blink. 400 ms on, 400 ms off — insistent without being an alarm. The slow one is half that
// rate: something is broken and needs a person, but nothing is waiting on the answer.
#define BLINK_TICKS 8
#define BLINK_SLOW_TICKS 16

// How long rgb_flash holds. Long enough to be seen, short enough not to read as a state.
#define FLASH_TICKS 3

// How long a gauge holds: a second. Long enough to look at after the hand has moved, short enough
// that the ring goes back to saying what the device is doing.
#define GAUGE_TICKS 20

const rgb_color_t RGB_OFF = {0, 0, 0};
const rgb_color_t RGB_WHITE = {255, 255, 255};
const rgb_color_t RGB_RED = {255, 0, 0};
const rgb_color_t RGB_GREEN = {0, 255, 0};
const rgb_color_t RGB_BLUE = {0, 0, 255};
// Deliberately faint. This is the resting state, and the device sits in a room people sleep in.
const rgb_color_t RGB_DIM_BLUE = {0, 0, 24};
const rgb_color_t RGB_CYAN = {0, 200, 255};
const rgb_color_t RGB_AMBER = {255, 120, 0};
const rgb_color_t RGB_MAGENTA = {255, 0, 200};

static led_strip_handle_t strip;

// What the renderer is drawing, and what it should go back to after a flash. Written by callers,
// read by the renderer, so both sides take the spinlock — the pair must change together or the ring
// briefly shows one state's colour in another state's pattern.
static portMUX_TYPE lock = portMUX_INITIALIZER_UNLOCKED;
static rgb_pattern_t want_pattern = RGB_SOLID;
static rgb_color_t want_color;
static rgb_color_t overlay_color;
static int overlay_left;
// -1 means a plain flash; 0..100 means draw that much of the ring.
static int overlay_gauge;

// scale dims a colour to `level` out of 255.
static rgb_color_t scale(rgb_color_t c, int level)
{
    return (rgb_color_t){
        .r = (uint8_t)(c.r * level / 255),
        .g = (uint8_t)(c.g * level / 255),
        .b = (uint8_t)(c.b * level / 255),
    };
}

// ramp returns a triangle over `period` ticks, squared.
//
// Squaring is not decoration. Perceived brightness is roughly the square root of emitted light, so a
// linear ramp looks like it lingers at the top and jumps at the bottom. Squaring the triangle
// cancels that out closely enough that the result reads as a breath rather than a sawtooth.
static int ramp(int phase, int period)
{
    int half = period / 2;
    int tri = phase < half ? phase * 255 / half : (period - phase) * 255 / half;
    return tri * tri / 255;
}

// paint writes one frame: every pixel, then one refresh. Never partial — a refresh per pixel is what
// made the old driver seven times as likely to collide with anything else touching the strip.
static void paint(const rgb_color_t *pixels)
{
    for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
        led_strip_set_pixel(strip, i, pixels[i].r, pixels[i].g, pixels[i].b);
    }
    // Return value ignored on purpose. A dropped frame is invisible at 20 Hz and the next tick fixes
    // it; aborting the firmware because an LED did not light is the wrong trade for a device whose
    // actual job is listening.
    led_strip_refresh(strip);
}

static void render(rgb_pattern_t pattern, rgb_color_t color, int tick)
{
    rgb_color_t px[LED_STRIP_LED_COUNT];

    switch (pattern) {
    case RGB_SOLID:
        for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
            px[i] = color;
        }
        break;

    case RGB_BREATHE:
    case RGB_BREATHE_SLOW: {
        int period = pattern == RGB_BREATHE ? BREATHE_TICKS : BREATHE_SLOW_TICKS;
        // Never fully dark at the bottom. A breath that reaches zero reads as a blink with a long
        // pause, which is a different signal — and on a device that says everything through seven
        // pixels, "off" has to keep meaning off.
        rgb_color_t c = scale(color, 20 + ramp(tick % period, period) * 235 / 255);
        for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
            px[i] = c;
        }
        break;
    }

    case RGB_SPIN: {
        // A bright head with a fading tail behind it, which is what makes the direction of travel
        // readable. A single lit pixel just looks like it is flickering somewhere on the ring.
        int head = (tick / SPIN_TICKS) % LED_STRIP_LED_COUNT;
        for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
            int behind = (head - i + LED_STRIP_LED_COUNT) % LED_STRIP_LED_COUNT;
            px[i] = scale(color, behind < 3 ? 255 >> behind : 0);
        }
        break;
    }

    case RGB_WAVE: {
        // A crest running round the ring, every pixel lit at some level. Reads as one thing moving
        // rather than several things blinking, which is what separates it from SPIN at a glance.
        int phase = tick % WAVE_TICKS;
        for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
            int at = (phase * LED_STRIP_LED_COUNT / WAVE_TICKS + i) % LED_STRIP_LED_COUNT;
            px[i] = scale(color, 40 + ramp(at, LED_STRIP_LED_COUNT) * 215 / 255);
        }
        break;
    }

    case RGB_BLINK:
    case RGB_BLINK_SLOW: {
        int half = pattern == RGB_BLINK ? BLINK_TICKS : BLINK_SLOW_TICKS;
        bool on = (tick / half) % 2 == 0;
        for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
            px[i] = on ? color : RGB_OFF;
        }
        break;
    }
    }

    paint(px);
}

// render_task is the only thing in the firmware that writes to the strip.
static void render_task(void *arg)
{
    for (int tick = 0;; tick++) {
        rgb_pattern_t pattern;
        rgb_color_t color;

        int gauge = -1;
        portENTER_CRITICAL(&lock);
        if (overlay_left > 0) {
            overlay_left--;
            pattern = RGB_SOLID;
            color = overlay_color;
            gauge = overlay_gauge;
        } else {
            pattern = want_pattern;
            color = want_color;
        }
        portEXIT_CRITICAL(&lock);

        if (gauge >= 0) {
            rgb_color_t px[LED_STRIP_LED_COUNT];
            // Rounded up, and never fewer than one: a ring showing nothing at all reads as broken
            // rather than as quiet, and the volume this device can be set to is never actually zero.
            int lit = (gauge * LED_STRIP_LED_COUNT + 99) / 100;
            if (lit < 1) {
                lit = 1;
            }
            for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
                // The unlit part stays faintly on, so the arc is read against a full ring rather
                // than against darkness — three lit pixels and four dark ones is a fraction; three
                // lit pixels alone is just three pixels.
                px[i] = i < lit ? color : scale(color, 12);
            }
            paint(px);
            vTaskDelay(pdMS_TO_TICKS(TICK_MS));
            continue;
        }

        render(pattern, color, tick);
        vTaskDelay(pdMS_TO_TICKS(TICK_MS));
    }
}

esp_err_t rgb_start(void)
{
    led_strip_config_t strip_config = {
        .strip_gpio_num = LED_STRIP_GPIO_PIN,
        .max_leds = LED_STRIP_LED_COUNT,
        .led_model = LED_MODEL_WS2812,
        .color_component_format = LED_STRIP_COLOR_COMPONENT_FMT_RGB,
        .flags = {.invert_out = false},
    };
    led_strip_rmt_config_t rmt_config = {
        .clk_src = RMT_CLK_SRC_DEFAULT,
        .resolution_hz = 10 * 1000 * 1000,
        .mem_block_symbols = 0,
        .flags = {.with_dma = 0},
    };

    esp_err_t err = led_strip_new_rmt_device(&strip_config, &rmt_config, &strip);
    if (err != ESP_OK) {
        return err;
    }
    want_color = RGB_OFF;

    // Core 0, with the network. Core 1 carries the audio front end's fetch loop, and a stall there
    // is what breaks echo cancellation — the ring is not worth a millisecond of that.
    if (xTaskCreatePinnedToCore(render_task, "rgb", 3 * 1024, NULL, 2, NULL, 0) != pdPASS) {
        return ESP_ERR_NO_MEM;
    }
    ESP_LOGI(TAG, "ring up (%d pixels)", LED_STRIP_LED_COUNT);
    return ESP_OK;
}

void rgb_show(rgb_pattern_t pattern, rgb_color_t color)
{
    portENTER_CRITICAL(&lock);
    want_pattern = pattern;
    want_color = color;
    portEXIT_CRITICAL(&lock);
}

void rgb_flash(rgb_color_t color)
{
    portENTER_CRITICAL(&lock);
    overlay_color = color;
    overlay_gauge = -1;
    overlay_left = FLASH_TICKS;
    portEXIT_CRITICAL(&lock);
}

void rgb_gauge(int percent, rgb_color_t color)
{
    portENTER_CRITICAL(&lock);
    overlay_color = color;
    overlay_gauge = percent < 0 ? 0 : percent > 100 ? 100 : percent;
    overlay_left = GAUGE_TICKS;
    portEXIT_CRITICAL(&lock);
}
