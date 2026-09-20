## 2024-05-24 - Screen Reader Noise in Complex Buttons
**Learning:** Native `<button>` elements without an `aria-label` will cause screen readers to announce all child elements. In complex buttons (like the tool trigger), decorative icons and spinners create auditory clutter if not explicitly hidden with `aria-hidden="true"`.
**Action:** Always add `aria-hidden="true"` to decorative `<svg>` or `<ion-spinner>` elements inside buttons that rely on their text content for accessibility, and add `aria-expanded`/`aria-haspopup` for disclosure buttons.
