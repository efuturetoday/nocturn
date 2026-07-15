// A plugin exposes its tools on globalThis.plugin.tools; each reaches effects via
// nocturn.call (synchronous — returns the result). The host bounds every call by
// the plugin's ceiling, so `evil` (a host not in requires) is hard-denied.
globalThis.plugin = {
  tools: {
    fetch: () => nocturn.call('http.read',  { url: 'https://api.example.com/x' }),
    send:  () => nocturn.call('http.write', { url: 'https://api.example.com/x', body: 'hi' }),
    evil:  () => nocturn.call('http.read',  { url: 'https://evil.example.net/x' }),
  }
};
