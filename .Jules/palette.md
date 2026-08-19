## 2026-08-19 - Standardize Popup Trigger Accessibility
**Learning:** When using Angular ternary logic for boolean ARIA attributes, they should be bound as strings (e.g. `[attr.aria-expanded]="open() ? 'true' : 'false'"`) to prevent them from being removed from the DOM when evaluated to false.
**Action:** Use string values for boolean attributes when utilizing `[attr.aria-*]` bindings, and remember to include `[attr.aria-haspopup]` on elements opening non-native dialogs or menus.
