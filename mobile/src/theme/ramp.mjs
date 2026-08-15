// Nocturn surface ramp generator.
//
// Ionic ships its stepped surface colours as pre-generated hex (a monotone lightness ramp from the
// base background toward the text colour) — NOT runtime color-mix. We do the same: this script emits
// the `--ion-background-color-step-*` block that lives in theme/variables.css, so the ramp is
// reproducible from (base hue, saturation curve, lightness range) instead of hand-guessed hex.
//
// "Dezent" profile: one consistent hue with a gentle low saturation that tapers toward the light end,
// so cards/items read as the base bg *lifted*, never as a separate, more-saturated purple box.
//
//   Regenerate:  node src/theme/ramp.mjs
//   then paste the output over the step block in theme/variables.css.

const HUE = 255; // matches the tint of --ion-background-color (#12101a)
const L0 = 9.5,
  L1 = 92; // lightness: just above the base (8) → near text
const S0 = 17,
  S1 = 6; // saturation tapers dark → light
const STEPS = [50, 100, 150, 200, 250, 300, 350, 400, 450, 500, 550, 600, 650, 700, 750, 800, 850, 900, 950];

function hslToHex(h, s, l) {
  s /= 100;
  l /= 100;
  const k = (n) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n) => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  const to = (x) =>
    Math.round(x * 255)
      .toString(16)
      .padStart(2, '0');
  return `#${to(f(0))}${to(f(8))}${to(f(4))}`;
}

STEPS.forEach((step, i) => {
  const t = i / (STEPS.length - 1);
  const l = L0 + (L1 - L0) * t;
  const s = S0 + (S1 - S0) * t;
  console.log(`  --ion-background-color-step-${step}: ${hslToHex(HUE, s, l)};`);
});
