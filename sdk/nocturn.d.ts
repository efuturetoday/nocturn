// Ambient type declarations for the Nocturn guest runtime.
//
// A plugin (or a code.run script) runs on an embedded QuickJS interpreter with a
// small prepended runtime (see internal/script/runtime.js). This file describes
// what that runtime exposes so you get autocomplete and type-checking — with a
// bundler+.ts OR just `// @ts-check` + JSDoc in plain .js (no build step).
//
// Point your tsconfig at it, e.g.:
//   { "compilerOptions": { "checkJs": true, "strict": true },
//     "include": ["plugin.ts", "path/to/nocturn.d.ts"] }
//
// SECURITY MODEL: nothing here grants authority. Every outward-facing call
// (fetch, fs) bottoms out at nocturn.call, which passes the host broker and
// out-of-band human approval. A denied effect throws.

export {}; // make this a module-free ambient file

declare global {
  // ------------------------------------------------------------------ core gate

  /** The JSON envelope every http.read / http.write returns. */
  interface HttpResponseEnvelope {
    status: number;
    statusText: string;
    headers: Record<string, string>;
    /** The response body as text. */
    body: string;
  }

  interface FileStat {
    exists: boolean;
    isDir: boolean;
    size: number;
  }

  interface FileEntry {
    name: string;
    isDir: boolean;
    size: number;
  }

  /** A clean, promise-based filesystem confined to the workspace, gated per call. */
  interface NocturnFs {
    readFile(path: string): Promise<string>;
    writeFile(path: string, data: string | Uint8Array): Promise<void>;
    list(path?: string): Promise<FileEntry[]>;
    stat(path: string): Promise<FileStat>;
    remove(path: string): Promise<void>;
  }

  interface Nocturn {
    /**
     * The one host gate. Synchronous: returns the tool's result as a string, or
     * throws (a denied/failed effect becomes a thrown Error). Prefer the fetch/fs
     * shims below; use call() for tools without a shim (e.g. dns.resolve).
     */
    call(tool: "http.read", args: { url: string; method?: "GET" | "HEAD" }): string;
    call(tool: "http.write", args: { url: string; method?: "POST" | "PUT" | "PATCH" | "DELETE"; body?: string; content_type?: string }): string;
    call(tool: "file.read", args: { path: string }): string;
    call(tool: "file.write", args: { path: string; content: string }): string;
    call(tool: "file.list", args: { path?: string }): string;
    call(tool: "file.stat", args: { path: string }): string;
    call(tool: "file.remove", args: { path: string }): string;
    call(tool: "dns.resolve", args: { host: string }): string;
    call(tool: string, args?: Record<string, unknown>): string;

    /** Purpose-built async filesystem (gated). */
    fs: NocturnFs;
  }

  const nocturn: Nocturn;

  // ------------------------------------------------------- plugin authoring contract

  type ToolResult = string | object | Promise<string | object>;

  interface NocturnPlugin {
    /** Each tool is `(args) => result`; the result is stringified to stdout. */
    tools: Record<string, (args: any) => ToolResult>;
  }

  /** Assign your plugin here: `globalThis.plugin = { tools: { ... } }`. */
  var plugin: NocturnPlugin;

  // ----------------------------------------------------------- Web/Node polyfills

  const console: { log(...a: any[]): void; error(...a: any[]): void };
  function print(...a: any[]): void;

  /** Base64 over a binary string (throws on code points > 0xff). */
  function btoa(data: string): string;
  function atob(data: string): string;

  class TextEncoder {
    encode(input?: string): Uint8Array;
  }
  class TextDecoder {
    decode(input?: Uint8Array | ArrayBuffer): string;
  }

  class URLSearchParams {
    constructor(init?: string | Record<string, string> | URLSearchParams);
    append(name: string, value: string): void;
    set(name: string, value: string): void;
    get(name: string): string | null;
    getAll(name: string): string[];
    has(name: string): boolean;
    delete(name: string): void;
    forEach(cb: (value: string, key: string, parent: URLSearchParams) => void, thisArg?: any): void;
    toString(): string;
  }

  /** Pragmatic http(s) URL parser — NOT fully WHATWG-compliant. */
  class URL {
    constructor(url: string, base?: string);
    protocol: string;
    username: string;
    password: string;
    host: string;
    hostname: string;
    port: string;
    pathname: string;
    search: string;
    hash: string;
    origin: string;
    href: string;
    searchParams: URLSearchParams;
    toString(): string;
  }

  /** Minimal Buffer: utf8 / base64 / base64url / hex only. */
  interface NocturnBuffer extends Uint8Array {
    toString(encoding?: "utf8" | "base64" | "base64url" | "hex"): string;
  }
  const Buffer: {
    from(data: string | Uint8Array, encoding?: "utf8" | "base64" | "base64url" | "hex"): NocturnBuffer;
  };

  class Headers {
    constructor(init?: Record<string, string> | [string, string][] | Headers);
    get(name: string): string | null;
    set(name: string, value: string): void;
    append(name: string, value: string): void;
    has(name: string): boolean;
    delete(name: string): void;
    forEach(cb: (value: string, key: string, parent: Headers) => void, thisArg?: any): void;
  }

  class FormData {
    append(name: string, value: string | Uint8Array, filename?: string): void;
    set(name: string, value: string | Uint8Array, filename?: string): void;
    get(name: string): string | Uint8Array | null;
    getAll(name: string): (string | Uint8Array)[];
    has(name: string): boolean;
    delete(name: string): void;
    forEach(cb: (value: any, key: string, parent: FormData) => void, thisArg?: any): void;
  }

  class Response {
    readonly status: number;
    readonly statusText: string;
    readonly headers: Headers;
    readonly ok: boolean;
    text(): Promise<string>;
    json(): Promise<any>;
    arrayBuffer(): Promise<ArrayBuffer>;
  }

  interface RequestInit {
    method?: string;
    /** Content-Type is honored; other request headers are NOT forwarded (the host owns the credential channel). */
    headers?: Record<string, string> | Headers;
    body?: string | object | URLSearchParams | FormData | Uint8Array;
  }

  /**
   * fetch routes through the gate: GET/HEAD → http.read, others → http.write.
   * A denied or failed request rejects with the thrown error.
   */
  function fetch(url: string | URL, init?: RequestInit): Promise<Response>;

  // --------------------------------------------------------------- fs (node-ish)

  interface Dirent {
    name: string;
    isDirectory(): boolean;
    isFile(): boolean;
  }
  interface Stats {
    size: number;
    isDirectory(): boolean;
    isFile(): boolean;
  }

  /**
   * A node:fs-shaped shim over the gated file.* tools, confined to the workspace.
   * Unsupported methods (mkdir, rename, streams, fds, watch) throw. Prefer nocturn.fs.
   */
  const fs: {
    readFileSync(path: string, encoding?: string): string;
    writeFileSync(path: string, data: string | Uint8Array): void;
    readdirSync(path?: string, opts?: { withFileTypes?: boolean }): string[] | Dirent[];
    statSync(path: string): Stats;
    existsSync(path: string): boolean;
    unlinkSync(path: string): void;
    rmSync(path: string, opts?: { recursive?: boolean }): void;
    promises: {
      readFile(path: string): Promise<string>;
      writeFile(path: string, data: string | Uint8Array): Promise<void>;
      readdir(path?: string, opts?: { withFileTypes?: boolean }): Promise<string[] | Dirent[]>;
      stat(path: string): Promise<Stats>;
      unlink(path: string): Promise<void>;
      rm(path: string, opts?: { recursive?: boolean }): Promise<void>;
    };
  };

  /** Minimal CommonJS shim: 'fs' | 'node:fs' | 'buffer' | 'util' (+ /promises). */
  function require(module: string): any;
}
