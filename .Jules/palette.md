## 2024-11-20 - Angular ARIA boolean bindings
**Learning:** In Angular templates, binding a boolean ARIA attribute directly (like `[attr.aria-expanded]="isOpen"`) causes the attribute to be completely removed from the DOM when the value is false, violating accessibility standards for attributes that should toggle between "true" and "false".
**Action:** Use a ternary string value for boolean ARIA attributes (e.g., `[attr.aria-expanded]="isOpen ? 'true' : 'false'"`) to ensure the attribute is correctly preserved in the DOM with the "false" string value.
