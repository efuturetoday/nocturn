// @ts-check
// Zero-build plugin authoring: plain JS + JSDoc, type-checked against nocturn.d.ts.
// This is the default — no bundler, no transpile. Nocturn loads this file as-is.
//
// A plugin defines its tools on globalThis.plugin.tools; each is `(args) => result`.
// Every fetch/fs call routes through the gate (broker + human approval); a denied
// effect throws. Declare the reachable hosts/paths in plugin.json (the cage).

globalThis.plugin = {
  tools: {
    /**
     * @param {{ city: string }} args
     * @returns {Promise<string>}
     */
    forecast: async (args) => {
      const r = await fetch("https://wttr.in/" + encodeURIComponent(args.city) + "?format=3");
      if (!r.ok) throw new Error("weather lookup failed: " + r.status);
      return await r.text();
    },
  },
};
