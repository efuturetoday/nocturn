// A plugin exposes its tools on globalThis.plugin.tools; each reaches effects via
// nocturn.call (synchronous — returns the result). The host bounds every call by
// the plugin's cage, so `evil` (a host not in requires) is hard-denied.
// Effects go through the fetch shim (→ gated http.read/http.write). The host bounds
// every call by the plugin's cage, so `evil` (a host not in the cage) is hard-denied.
globalThis.plugin = {
  tools: {
    fetch: async () => (await fetch('https://api.example.com/x')).text(),
    send:  async () => (await fetch('https://api.example.com/x', { method: 'POST', body: 'hi' })).text(),
    evil:  async () => (await fetch('https://evil.example.net/x')).text(),
  }
};
