#include "rgb_led_driver.h"

#include <math.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "bsp_board.h"
#include "esp_check.h"
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

// How fast the speech level falls when nothing new is reported, per tick, in 1/256ths.
//
// 216/256 is about a third left after five ticks — a quarter of a second. Speech is full of stops
// between words that are shorter than that, so they never reach the ring; a real pause does. The
// attack is not damped at all: a voice starting is the moment worth being immediate about.
#define LEVEL_DECAY 216

// Where the level sits when nobody is reporting one, out of 255. RGB_SIRI never freezes: a ring
// that stood still during a reply the board happens not to be measuring would read as a hang.
#define LEVEL_FLOOR 40

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

// The four colours RGB_SIRI is made of. Not a palette anyone picks from — the look IS these four
// overlapping, and the hues that carry it (the violet between blue and pink, the sea-green between
// teal and blue) exist only where two of them add. Which is also why it suits this device: the
// diffuser blends the ring a second time, after the arithmetic already did.
static const rgb_color_t SIRI_LOBES[] = {
    {59, 125, 255},  // blue
    {164, 75, 255},  // purple
    {255, 61, 139},  // pink
    {35, 211, 211},  // teal
};
#define SIRI_LOBE_COUNT (sizeof(SIRI_LOBES) / sizeof(SIRI_LOBES[0]))

// How loud the speaker is, 0..255. Written by whoever moves the samples, decayed by the renderer.
// A single byte, so neither side needs the lock: the renderer reading a value one tick stale is
// invisible, and a torn read is impossible.
static volatile uint8_t level;

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

// siri draws four coloured lobes on the ring and ADDS them, per channel.
//
// The sum is the whole trick. Nobody sees four blobs; what reads is where blue slides through purple
// into pink, and that hue exists only because the channels add where two lobes overlap. A palette
// rotated around the ring would give bands. This gives the drifting, liquid thing.
//
// float and not the integer arithmetic the rest of this file uses, deliberately: the S3 has a
// hardware FPU, this is 28 expf and 8 sinf per frame at 20 Hz, and the alternative is three lookup
// tables to save microseconds on a core whose real work is the network. The integer style elsewhere
// buys determinism in patterns a person compares side by side; here it would buy nothing.
//
// `drift` is revolutions per tick, `together` makes every lobe travel the same way — one object
// circling, rather than something alive and undirected.
static void siri(rgb_color_t *px, int tick, float drift, float breathe, float width, float gain,
                 float floor_blue, bool together)
{
    float acc[LED_STRIP_LED_COUNT][3] = {{0}};

    for (int j = 0; j < (int)SIRI_LOBE_COUNT; j++) {
        float dir = together ? 1.0f : (j % 2 ? -1.0f : 1.0f);
        float pos = fmodf(tick * drift * (1.0f + j * 0.22f) * dir * LED_STRIP_LED_COUNT
                              + j * (float)LED_STRIP_LED_COUNT / SIRI_LOBE_COUNT,
                          LED_STRIP_LED_COUNT);
        if (pos < 0) {
            pos += LED_STRIP_LED_COUNT;
        }
        float w = width * (0.85f + 0.3f * sinf(tick * 0.017f + j * 1.9f));
        if (w < 0.35f) {
            w = 0.35f;
        }
        float amp = gain * (0.55f + 0.45f * sinf(tick * breathe * (1.0f + j * 0.3f) + j * 2.4f));
        if (amp <= 0) {
            continue;
        }
        for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
            // Distance the short way round: a lobe at pixel 6 lights pixel 0, because the strip is
            // a ring and the seam between its ends is not a wall.
            float raw = fabsf(i - pos);
            float d = raw < LED_STRIP_LED_COUNT - raw ? raw : LED_STRIP_LED_COUNT - raw;
            float k = amp * expf(-(d * d) / (2.0f * w * w));
            acc[i][0] += SIRI_LOBES[j].r * k;
            acc[i][1] += SIRI_LOBES[j].g * k;
            acc[i][2] += SIRI_LOBES[j].b * k;
        }
    }

    // The floor is blue rather than grey. Between two lobes the ring must not go dark — that would
    // read as gaps — but it must not go neutral either: a white residue is what makes cheap RGB look
    // cheap, and this whole pattern is a claim about colour.
    for (int i = 0; i < LED_STRIP_LED_COUNT; i++) {
        float r = acc[i][0] + 64.0f * floor_blue;
        float g = acc[i][1] + 38.0f * floor_blue;
        float b = acc[i][2] + 255.0f * floor_blue;
        px[i].r = r > 255.0f ? 255 : (uint8_t)r;
        px[i].g = g > 255.0f ? 255 : (uint8_t)g;
        px[i].b = b > 255.0f ? 255 : (uint8_t)b;
    }
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

    case RGB_SIRI: {
        // The one pattern driven from outside: loudness sets how far the lobes swell and how wide
        // they spread, so a loud syllable is a wide bright surge and a quiet one barely moves. The
        // colour argument is unused — see the header.
        float loud = level / 255.0f;
        siri(px, tick, 0.010f, 0.05f, 0.85f + 0.35f * loud, 0.45f + 0.55f * loud, 0.05f, false);
        break;
    }

    case RGB_SIRI_THINK:
        // Same colours, pulled tight and all circling one way: unmistakably the same device doing
        // something else. Deaf to the level on purpose — nothing is being said yet.
        siri(px, tick, 0.034f, 0.02f, 0.6f, 0.85f, 0.03f, true);
        break;
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

        // The release side of the envelope. Decayed here rather than by the reporter, because the
        // frame is what it is for: whoever writes the level knows when a chunk was loud, not how
        // long the ring should still show it.
        uint8_t now = level;
        uint8_t next = (uint8_t)((now * LEVEL_DECAY) / 256);
        level = next < LEVEL_FLOOR ? LEVEL_FLOOR : next;

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

    ESP_RETURN_ON_ERROR(led_strip_new_rmt_device(&strip_config, &rmt_config, &strip), TAG,
                        "Failed to create LED strip");
    want_color = RGB_OFF;

    // Core 0, with the network. Core 1 carries the audio front end's fetch loop, and a stall there
    // is what breaks echo cancellation — the ring is not worth a millisecond of that.
    ESP_RETURN_ON_FALSE(xTaskCreatePinnedToCore(render_task, "rgb", 3 * 1024, NULL, 2, NULL, 0) == pdPASS,
                        ESP_ERR_NO_MEM, TAG, "Failed to create render task");
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

void rgb_level(uint8_t value)
{
    // Attack only. A quieter chunk never pulls the ring down at once — that is the renderer's decay,
    // and letting a single quiet 20 ms window do it would flicker on every consonant.
    if (value > level) {
        level = value;
    }
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
