## 2024-08-16 - ARIA Expanded and HasPopup for Tool Buttons
**Learning:** Found that `<button>` triggers opening an `<ion-modal>` (for tools accordion) inside the chat message bubbles lacked ARIA states indicating that it opens a dialog and its current expanded state, which is critical for screen reader accessibility in messaging apps.
**Action:** Used Angular's `[attr.aria-expanded]="isOpenSignal() ? 'true' : 'false'"` binding strategy combined with `aria-haspopup="dialog"` to properly connect the button with the modal state.
