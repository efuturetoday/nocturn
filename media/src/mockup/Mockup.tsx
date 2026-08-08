import React from "react";
import { AbsoluteFill, spring, useCurrentFrame, useVideoConfig } from "remotion";
import { beat, pulse, ramp } from "../anim";
import { color, font } from "../theme";

/**
 * The canvas IS the screen — 1179×2556, an iPhone 15 Pro at native resolution.
 * No device frame, no margin: what renders is what a screenshot would show.
 */
const H = 2556;
const SHEET_TOP = 1700;
const SHEET_H = H - SHEET_TOP;

/** How far the chat rides up so the sheet never covers the last line. */
const CHAT_REST = 180;
const CHAT_LIFT = SHEET_H + 56 - CHAT_REST;

/* -------------------------------------------------------------- the chat */

const Bubble: React.FC<{
  from: "you" | "it";
  opacity?: number;
  rise?: number;
  children: React.ReactNode;
}> = ({ from, opacity = 1, rise = 0, children }) => {
  const mine = from === "you";
  return (
    <div
      style={{
        display: "flex",
        justifyContent: mine ? "flex-end" : "flex-start",
        marginBottom: 26,
        opacity,
        transform: `translateY(${rise}px)`,
      }}
    >
      <div
        style={{
          maxWidth: "78%",
          padding: "24px 32px",
          borderRadius: 32,
          borderBottomRightRadius: mine ? 10 : 32,
          borderBottomLeftRadius: mine ? 32 : 10,
          background: mine ? "#1d3a6b" : "#151d30",
          fontFamily: font.sans,
          fontSize: 38,
          lineHeight: 1.35,
          color: mine ? color.text : "#cfd8e8",
        }}
      >
        {children}
      </div>
    </div>
  );
};

/** The line that is still running while the ask is open. */
const Working: React.FC<{ opacity: number }> = ({ opacity }) => {
  const frame = useCurrentFrame();
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 14,
        paddingLeft: 8,
        height: 46,
        opacity,
      }}
    >
      {[0, 1, 2].map((i) => (
        <span
          key={i}
          style={{
            width: 13,
            height: 13,
            borderRadius: 7,
            background: color.faint,
            opacity: 0.3 + 0.7 * (0.5 + 0.5 * Math.sin((frame - i * 5) / 5.5)),
          }}
        />
      ))}
      <span
        style={{
          marginLeft: 8,
          fontFamily: font.sans,
          fontSize: 32,
          color: color.faint,
        }}
      >
        looking it up
      </span>
    </div>
  );
};

/* --------------------------------------------------------------- the ask */

const Button: React.FC<{
  label: string;
  kind: "allow" | "deny";
  press?: number;
}> = ({ label, kind, press = 0 }) => {
  const allow = kind === "allow";
  const lit = press > 0.001;
  return (
    <div
      style={{
        position: "relative",
        height: 100,
        borderRadius: 26,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        border: allow ? `2px solid ${lit ? color.ok : "#2c3c5c"}` : "none",
        background: lit
          ? `rgba(95,211,160,${0.08 + 0.16 * press})`
          : allow
            ? "rgba(255,255,255,0.03)"
            : "#1d2740",
        transform: `scale(${1 - 0.02 * press})`,
        fontFamily: font.sans,
        fontSize: 37,
        fontWeight: kind === "deny" ? 600 : 400,
        color: lit ? color.ok : kind === "deny" ? color.text : "#c6d2e6",
      }}
    >
      {label}
    </div>
  );
};

/* ---------------------------------------------------------------- screen */

export const Mockup: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();

  // The sheet rises with weight, and leaves without ceremony.
  const rise = spring({
    frame: frame - beat.sheetUp,
    fps,
    config: { damping: 26, mass: 0.9, stiffness: 130 },
  });
  const leave = ramp(frame, beat.sheetDown, 16);
  const shown = Math.max(0, Math.min(1, rise - leave));

  const press = pulse(frame, beat.tap, 26);
  const answered = ramp(frame, beat.answer, 14);

  return (
    <AbsoluteFill style={{ background: "#0b0f19" }}>
      {/* status bar */}
      <div
        style={{
          height: 130,
          padding: "0 62px 20px",
          display: "flex",
          alignItems: "flex-end",
          justifyContent: "space-between",
          fontFamily: font.sans,
          fontSize: 36,
          fontWeight: 600,
          color: color.text,
        }}
      >
        <span>21:47</span>
        <span style={{ display: "flex", alignItems: "flex-end", gap: 5 }}>
          {[13, 19, 25, 31].map((h) => (
            <span
              key={h}
              style={{ width: 8, height: h, borderRadius: 2, background: color.text }}
            />
          ))}
          <span
            style={{
              marginLeft: 16,
              width: 62,
              height: 30,
              borderRadius: 8,
              border: `3px solid ${color.muted}`,
              padding: 4,
            }}
          >
            <span
              style={{
                display: "block",
                width: "70%",
                height: "100%",
                borderRadius: 3,
                background: color.text,
              }}
            />
          </span>
        </span>
      </div>

      {/* dynamic island */}
      <div
        style={{
          position: "absolute",
          left: "50%",
          top: 32,
          transform: "translateX(-50%)",
          width: 260,
          height: 76,
          borderRadius: 38,
          background: "#000",
        }}
      />

      {/* app header */}
      <div
        style={{
          height: 124,
          borderBottom: "1px solid #1a2437",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          fontFamily: font.sans,
          fontSize: 40,
          fontWeight: 600,
          color: color.text,
        }}
      >
        nocturn
      </div>

      {/* The conversation, bottom-anchored the way a messenger stacks it, and
          lifted while the sheet is up so the last line stays visible. */}
      <div
        style={{
          position: "absolute",
          left: 0,
          right: 0,
          bottom: CHAT_REST,
          padding: "0 44px",
          transform: `translateY(${-CHAT_LIFT * shown}px)`,
        }}
      >
        <Bubble from="you">what&rsquo;s the weather?</Bubble>
        <Bubble from="it">Checking now.</Bubble>
        {answered > 0.001 ? (
          <Bubble from="it" opacity={answered} rise={(1 - answered) * 26}>
            18°, light rain until this evening.
          </Bubble>
        ) : (
          <Working opacity={1} />
        )}
      </div>

      {/* what the sheet dims */}
      <div
        style={{
          position: "absolute",
          inset: 0,
          background: "rgba(3,5,10,0.62)",
          opacity: shown,
        }}
      />

      {/* the ask */}
      <div
        style={{
          position: "absolute",
          left: 0,
          right: 0,
          top: SHEET_TOP,
          bottom: 0,
          transform: `translateY(${(1 - shown) * (SHEET_H + 20)}px)`,
          background: "#121a2b",
          borderTop: "1px solid #24304a",
          borderTopLeftRadius: 52,
          borderTopRightRadius: 52,
          padding: "28px 48px 84px",
          boxShadow: "0 -40px 100px rgba(0,0,0,0.6)",
        }}
      >
        <div
          style={{
            width: 68,
            height: 7,
            borderRadius: 4,
            background: "#33405c",
            margin: "0 auto 40px",
          }}
        />

        <div
          style={{
            fontFamily: font.sans,
            fontSize: 29,
            letterSpacing: 2.2,
            textTransform: "uppercase",
            color: color.human,
          }}
        >
          nocturn wants to
        </div>

        <div
          style={{
            marginTop: 22,
            fontFamily: font.sans,
            fontSize: 52,
            lineHeight: 1.28,
            fontWeight: 600,
            color: color.text,
          }}
        >
          Send data to
          <br />
          <span style={{ fontFamily: font.mono, fontSize: 46 }}>
            api.weather.example
          </span>
        </div>

        <div
          style={{ marginTop: 44, display: "flex", flexDirection: "column", gap: 18 }}
        >
          <Button label="Allow once" kind="allow" press={press} />
          <Button label="Allow for this session" kind="allow" />
          <Button label="Always allow" kind="allow" />
          <Button label="Don't allow" kind="deny" />
        </div>
      </div>

      {/* home indicator */}
      <div
        style={{
          position: "absolute",
          left: "50%",
          bottom: 20,
          transform: "translateX(-50%)",
          width: 290,
          height: 10,
          borderRadius: 5,
          background: "#5a6a86",
        }}
      />
    </AbsoluteFill>
  );
};
