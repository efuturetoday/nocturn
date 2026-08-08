## 2026-08-08 - Accessible Icon-Only Buttons
**Learning:** Icon-only buttons in Angular/Ionic components (`<ion-button>` with an `<ion-icon>`) can be inaccessible to screen readers without an `aria-label`.
**Action:** When creating or modifying icon-only buttons, especially in shared components like `ComposerComponent`, ensure an `aria-label` attribute is added directly to the `<ion-button>` element to describe its action.
