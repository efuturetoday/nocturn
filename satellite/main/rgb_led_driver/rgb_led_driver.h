#pragma once

#include <stdint.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// The LED ring: the satellite's entire output.
//
// A screenless device says everything it has to say through seven pixels, so the ring shows a STATE
// rather than an animation for its own sake. Each pattern below has to be tellable from every other
// one across a room, at a glance, by someone who is not looking for it.
//
// ONE task owns the strip and nothing else touches it. The vendor demo had a task rendering its own
// modes while callers wrote pixels directly, and both called led_strip_refresh — which internally
// enables the RMT channel, transmits, and disables it again. Two of those overlapping is a channel
// enabled twice:
//
//   E rmt: rmt_tx_enable(765): channel not in init state
//   E led_strip_rmt: led_strip_rmt_refresh(87): enable RMT channel failed
//
// which cost the first colour after boot every time. Worse, the demo's own path wrapped that refresh
// in ESP_ERROR_CHECK, so losing the race was an abort waiting to happen rather than a dropped frame.

typedef struct {
    uint8_t r, g, b;
} rgb_color_t;

// The colours the states use. Named rather than raw triples so a state's meaning and its appearance
// stay in one place — see rgb_led_driver.c for which state gets which, and why.
extern const rgb_color_t RGB_OFF;
extern const rgb_color_t RGB_WHITE;
extern const rgb_color_t RGB_RED;
extern const rgb_color_t RGB_GREEN;
extern const rgb_color_t RGB_BLUE;
extern const rgb_color_t RGB_DIM_BLUE;
extern const rgb_color_t RGB_CYAN;
extern const rgb_color_t RGB_AMBER;
extern const rgb_color_t RGB_MAGENTA;

// The patterns, and the pace is part of the meaning rather than a parameter.
//
// Two speeds exist because two different things are being said. A slow breath is patience — the
// device is fine and is waiting on something that is not you. A normal breath is an invitation —
// it is waiting on YOU. Same shape, and telling them apart across a room is entirely the tempo, so
// the tempo cannot be a knob someone passes by accident.
//
// Likewise the two blinks: slow means a person has to come and fix something, quick means a person
// has to come and DECIDE something. The second is the more urgent and reads that way.
typedef enum {
    RGB_SOLID,        // steady. presence without motion
    RGB_BREATHE,      // rise and fall, ~2 s. alive, waiting on you
    RGB_BREATHE_SLOW, // rise and fall, ~4 s. alive, waiting on something else
    RGB_SPIN,         // one pixel travelling the ring. working on something
    RGB_WAVE,         // a brightness crest moving round. talking
    RGB_BLINK,        // on/off, ~0.8 s. wants a decision
    RGB_BLINK_SLOW,   // on/off, ~1.6 s. wants repair
    RGB_SIRI,         // four drifting colours, loudness-driven. the assistant is speaking
    RGB_SIRI_THINK,   // the same colours pulled tight and circling. it is working
} rgb_pattern_t;

// THE TWO SIRI PATTERNS CARRY THEIR OWN COLOURS and ignore the colour passed to rgb_show.
//
// They have to. What makes that look recognisable is not a shape but the mixture — blue sliding
// through purple into pink — and a mixture cannot be tinted by one triple without becoming a
// different thing. Call them with RGB_OFF so the call site shows that the argument is unused.
//
// They are also the only two patterns that react to anything outside themselves: RGB_SIRI is driven
// by rgb_level below, so the ring swells with the actual speech instead of running an animation that
// merely coincides with it.

// rgb_start brings up the strip and its renderer. Call once, before anything else here.
esp_err_t rgb_start(void);

// rgb_show sets what the ring displays until the next call. Pattern and colour change together, so
// the renderer never draws one against the other.
void rgb_show(rgb_pattern_t pattern, rgb_color_t color);

// rgb_level reports how loud the speaker is RIGHT NOW, 0..255, and RGB_SIRI swells with it.
//
// Called from whoever moves the samples, because that is the only party that knows. Routing this
// through the state module would mean an event every 20 ms carrying a number that is stale by the
// next frame — and state is about what the device IS, while this is about how loud it is being.
//
// Attack is immediate and release is a decay in the renderer: speech is full of short gaps, and a
// ring that dropped into every one of them would read as flicker rather than as a voice. Stop
// calling this and the level falls to a floor on its own, so a reply the board is not measuring
// still animates.
void rgb_level(uint8_t level);

// rgb_flash interrupts with a brief full-brightness pulse, then restores whatever rgb_show last set.
//
// For the things that are moments rather than states — a barge-in registering, a link coming up.
// Giving those a state of their own would mean a state the machine leaves on the next tick.
void rgb_flash(rgb_color_t color);

// rgb_gauge shows a value as a filled arc of the ring, then restores what rgb_show last set.
//
// For the things a person adjusts and needs to see the result of at once — volume, and nothing else
// so far. A number has to be readable in the half second before the hand leaves the button, which
// rules out anything that has to be counted: the arc is read by how far round it goes.
//
// Held for about a second, because it is feedback on an action rather than a state, and because
// pressing again while it shows simply restarts the hold.
void rgb_gauge(int percent, rgb_color_t color);

#ifdef __cplusplus
}
#endif
