## 2026-08-21 - Modal triggers missing aria-expanded
**Learning:** Icon/label-only buttons that trigger `ion-modal` overlays (like the tool calls accordion trigger) need explicit `aria-expanded` attributes. Also noticed the importance of using ternary string evaluation `[attr.aria-expanded]="open() ? 'true' : 'false'"` in Angular to prevent the DOM node attribute from being removed entirely when it is false.
**Action:** When implementing any button that opens a modal or accordion, explicitly add `[attr.aria-expanded]` and `aria-label` to provide context for screen readers.
