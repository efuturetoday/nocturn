// Optional TypeScript authoring. TypeScript must be transpiled to plain JS before
// Nocturn loads it (the guest runs JS, not TS). Build it yourself, e.g.:
//
//   esbuild plugin.ts --bundle --format=iife --outfile=plugin.js
//   # or, no bundler:  tsc plugin.ts --outFile plugin.js --target ES2020
//
// Commit the produced plugin.js. Types come from ../nocturn.d.ts (see tsconfig.json).
// With a bundler you can `import` npm packages; they get inlined into plugin.js.
// (fetch/fs still route through the gate at runtime — that is provided by Nocturn.)

interface ForecastArgs {
  city: string;
}

globalThis.plugin = {
  tools: {
    forecast: async (args: ForecastArgs): Promise<string> => {
      const r = await fetch("https://wttr.in/" + encodeURIComponent(args.city) + "?format=3");
      if (!r.ok) throw new Error("weather lookup failed: " + r.status);
      return await r.text();
    },
  },
};
