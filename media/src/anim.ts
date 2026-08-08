import { interpolate } from "remotion";

export const FPS = 30;

/**
 * A one-shot demo, not a loop: it opens on a question already in flight and
 * ends holding on the answer. Absolute start frames.
 */
export const beat = {
  work: 0, // the assistant is looking something up
  sheetUp: 40, // it needs permission, and asks
  tap: 160, // answered
  sheetDown: 192, // the ask goes away
  answer: 225, // the result lands in the chat
  end: 360,
} as const;

export const DURATION = beat.end;

/** Linear 0→1 ramp, clamped. */
export const ramp = (frame: number, start: number, duration = 12) =>
  interpolate(frame, [start, start + duration], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });

/** 0→1→0. For things that flash once and are gone. */
export const pulse = (frame: number, start: number, duration = 20) => {
  const half = duration / 2;
  return ramp(frame, start, half) * (1 - ramp(frame, start + half, half));
};
