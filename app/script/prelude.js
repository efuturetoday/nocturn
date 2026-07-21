// Nocturn guest runtime — a small, hand-written prelude prepended to every JS
// program (code.run scripts AND plugins) before it is evaluated.
//
// It provides a familiar SUBSET of Web/Node APIs. The PURE-COMPUTE ones (btoa,
// TextEncoder, URL, Buffer, …) are plain polyfills. The OUTWARD-FACING ones
// (fetch, fs) bottom out at globalThis.nocturn.call, so every action they perform
// still passes the broker + out-of-band HITL at the one gate.
//
// SECURITY: this runs INSIDE the sandbox guest with no more authority than plugin
// code. A buggy or malicious shim here can do nothing nocturn.call does not
// already allow — it is DevEx sugar, not a security boundary. The reference
// monitor is Guard.Authorize at the host, unchanged.
(function (g) {
  "use strict";
  if (g.__nocturnRuntime) return; // idempotent: safe if prepended twice
  g.__nocturnRuntime = true;

  var B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

  // --- base64 over a binary string (btoa/atob semantics) ---
  function btoa(bin) {
    bin = String(bin);
    var out = "", i = 0, n = bin.length;
    while (i < n) {
      var c1 = bin.charCodeAt(i++);
      var c2 = i <= n - 1 ? bin.charCodeAt(i++) : NaN;
      var c3 = i <= n - 1 ? bin.charCodeAt(i++) : NaN;
      if (c1 > 0xff || (c2 === c2 && c2 > 0xff) || (c3 === c3 && c3 > 0xff)) {
        throw new Error("btoa: string contains a character outside the 0x00..0xff range");
      }
      var e1 = c1 >> 2;
      var e2 = ((c1 & 3) << 4) | ((c2 === c2 ? c2 : 0) >> 4);
      var e3 = c2 === c2 ? (((c2 & 15) << 2) | ((c3 === c3 ? c3 : 0) >> 6)) : 64;
      var e4 = c3 === c3 ? (c3 & 63) : 64;
      out += B64[e1] + B64[e2] + (e3 === 64 ? "=" : B64[e3]) + (e4 === 64 ? "=" : B64[e4]);
    }
    return out;
  }

  function atob(b64) {
    b64 = String(b64).replace(/[ \t\r\n\f]/g, "").replace(/=+$/, "");
    if (b64.length % 4 === 1) throw new Error("atob: invalid base64 length");
    var out = "", buffer = 0, bits = 0;
    for (var i = 0; i < b64.length; i++) {
      var idx = B64.indexOf(b64.charAt(i));
      if (idx === -1) throw new Error("atob: invalid base64 character");
      buffer = (buffer << 6) | idx;
      bits += 6;
      if (bits >= 8) {
        bits -= 8;
        out += String.fromCharCode((buffer >> bits) & 0xff);
      }
    }
    return out;
  }

  // --- UTF-8 encode/decode ---
  function TextEncoder() {}
  TextEncoder.prototype.encode = function (str) {
    str = String(str == null ? "" : str);
    var bytes = [];
    for (var i = 0; i < str.length; i++) {
      var c = str.charCodeAt(i);
      if (c < 0x80) {
        bytes.push(c);
      } else if (c < 0x800) {
        bytes.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
      } else if (c >= 0xd800 && c <= 0xdbff && i + 1 < str.length) {
        var cp = 0x10000 + ((c & 0x3ff) << 10) + (str.charCodeAt(++i) & 0x3ff);
        bytes.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
      } else {
        bytes.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
      }
    }
    return new Uint8Array(bytes);
  };

  function TextDecoder() {}
  TextDecoder.prototype.decode = function (buf) {
    var bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf || []);
    var out = "", i = 0, n = bytes.length;
    while (i < n) {
      var b = bytes[i++];
      if (b < 0x80) {
        out += String.fromCharCode(b);
      } else if (b >= 0xc0 && b < 0xe0) {
        out += String.fromCharCode(((b & 0x1f) << 6) | (bytes[i++] & 0x3f));
      } else if (b >= 0xe0 && b < 0xf0) {
        out += String.fromCharCode(((b & 0x0f) << 12) | ((bytes[i++] & 0x3f) << 6) | (bytes[i++] & 0x3f));
      } else {
        var cp = (((b & 0x07) << 18) | ((bytes[i++] & 0x3f) << 12) | ((bytes[i++] & 0x3f) << 6) | (bytes[i++] & 0x3f)) - 0x10000;
        out += String.fromCharCode(0xd800 + (cp >> 10), 0xdc00 + (cp & 0x3ff));
      }
    }
    return out;
  };

  function bytesToBin(u8) {
    var s = "";
    for (var i = 0; i < u8.length; i++) s += String.fromCharCode(u8[i]);
    return s;
  }
  function binToBytes(bin) {
    var u8 = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i) & 0xff;
    return u8;
  }
  var HEX = "0123456789abcdef";

  // --- minimal Buffer (utf8 / base64 / base64url / hex only) ---
  var Buffer = {
    from: function (x, enc) {
      if (x instanceof Uint8Array) return tagBuffer(x.slice());
      var s = String(x);
      if (enc === "base64" || enc === "base64url") {
        return tagBuffer(binToBytes(atob(s.replace(/-/g, "+").replace(/_/g, "/"))));
      }
      if (enc === "hex") {
        var u8 = new Uint8Array(s.length / 2);
        for (var i = 0; i < u8.length; i++) u8[i] = parseInt(s.substr(i * 2, 2), 16);
        return tagBuffer(u8);
      }
      return tagBuffer(new TextEncoder().encode(s)); // utf8 default
    },
  };
  function tagBuffer(u8) {
    u8.toString = function (enc) {
      if (enc === "base64") return btoa(bytesToBin(this));
      if (enc === "base64url") return btoa(bytesToBin(this)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
      if (enc === "hex") {
        var s = "";
        for (var i = 0; i < this.length; i++) s += HEX[this[i] >> 4] + HEX[this[i] & 0xf];
        return s;
      }
      return new TextDecoder().decode(this); // utf8 default
    };
    return u8;
  }

  // --- URLSearchParams ---
  function URLSearchParams(init) {
    this._p = [];
    if (init instanceof URLSearchParams) {
      this._p = init._p.slice();
    } else if (typeof init === "string") {
      var s = init.charAt(0) === "?" ? init.slice(1) : init;
      var self = this;
      if (s) s.split("&").forEach(function (pair) {
        var eq = pair.indexOf("=");
        var k = eq === -1 ? pair : pair.slice(0, eq);
        var v = eq === -1 ? "" : pair.slice(eq + 1);
        self._p.push([decodeURIComponent(k.replace(/\+/g, " ")), decodeURIComponent(v.replace(/\+/g, " "))]);
      });
    } else if (init && typeof init === "object") {
      for (var k in init) if (Object.prototype.hasOwnProperty.call(init, k)) this._p.push([String(k), String(init[k])]);
    }
  }
  URLSearchParams.prototype.append = function (k, v) { this._p.push([String(k), String(v)]); };
  URLSearchParams.prototype.set = function (k, v) { this.delete(k); this.append(k, v); };
  URLSearchParams.prototype.get = function (k) { for (var i = 0; i < this._p.length; i++) if (this._p[i][0] === k) return this._p[i][1]; return null; };
  URLSearchParams.prototype.getAll = function (k) { return this._p.filter(function (p) { return p[0] === k; }).map(function (p) { return p[1]; }); };
  URLSearchParams.prototype.has = function (k) { return this.get(k) !== null; };
  URLSearchParams.prototype.delete = function (k) { this._p = this._p.filter(function (p) { return p[0] !== k; }); };
  URLSearchParams.prototype.forEach = function (fn, self) { this._p.forEach(function (p) { fn.call(self, p[1], p[0], this); }, this); };
  // application/x-www-form-urlencoded serialization: like encodeURIComponent but
  // spaces become "+" (and a literal "+" in input is already %2B via encode).
  function formEncode(s) { return encodeURIComponent(s).replace(/%20/g, "+"); }
  URLSearchParams.prototype.toString = function () {
    return this._p.map(function (p) { return formEncode(p[0]) + "=" + formEncode(p[1]); }).join("&");
  };

  // --- URL (pragmatic http(s) parser; NOT fully WHATWG-compliant) ---
  function URL(url, base) {
    var s = String(url);
    if (base != null && !/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(s)) {
      var b = new URL(base);
      s = s.charAt(0) === "/" ? b.origin + s : b.origin + b.pathname.replace(/[^/]*$/, "") + s;
    }
    var m = /^([a-zA-Z][a-zA-Z0-9+.-]*:)\/\/([^/?#]*)([^?#]*)(\?[^#]*)?(#.*)?$/.exec(s);
    if (!m) throw new TypeError("Invalid URL: " + url);
    this.protocol = m[1];
    var authority = m[2], at = authority.lastIndexOf("@");
    this.username = ""; this.password = "";
    if (at !== -1) {
      var cred = authority.slice(0, at).split(":");
      this.username = cred[0] || ""; this.password = cred[1] || "";
      authority = authority.slice(at + 1);
    }
    var colon = authority.lastIndexOf(":");
    if (colon !== -1 && authority.indexOf("]") < colon) {
      this.hostname = authority.slice(0, colon); this.port = authority.slice(colon + 1);
    } else {
      this.hostname = authority; this.port = "";
    }
    this.host = authority;
    this.pathname = m[3] || "/";
    this.search = m[4] || "";
    this.hash = m[5] || "";
    this.searchParams = new URLSearchParams(this.search);
    this.origin = this.protocol + "//" + this.host;
    this.href = this.origin + this.pathname + this.search + this.hash;
  }
  URL.prototype.toString = function () { return this.href; };

  // --- Headers (case-insensitive) ---
  function Headers(init) {
    this._h = {};
    var self = this;
    if (init instanceof Headers) init.forEach(function (v, k) { self.set(k, v); });
    else if (Array.isArray(init)) init.forEach(function (p) { self.append(p[0], p[1]); });
    else if (init && typeof init === "object") for (var k in init) if (Object.prototype.hasOwnProperty.call(init, k)) this.set(k, init[k]);
  }
  Headers.prototype.get = function (k) { var v = this._h[String(k).toLowerCase()]; return v === undefined ? null : v; };
  Headers.prototype.set = function (k, v) { this._h[String(k).toLowerCase()] = String(v); };
  Headers.prototype.append = function (k, v) { var lk = String(k).toLowerCase(); this._h[lk] = this._h[lk] === undefined ? String(v) : this._h[lk] + ", " + v; };
  Headers.prototype.has = function (k) { return this._h[String(k).toLowerCase()] !== undefined; };
  Headers.prototype.delete = function (k) { delete this._h[String(k).toLowerCase()]; };
  Headers.prototype.forEach = function (fn, self) { for (var k in this._h) if (Object.prototype.hasOwnProperty.call(this._h, k)) fn.call(self, this._h[k], k, this); };

  // --- Response ---
  function Response(env) {
    env = env || {};
    this.status = env.status || 0;
    this.statusText = env.statusText || "";
    this.headers = new Headers(env.headers || {});
    this.ok = this.status >= 200 && this.status < 300;
    this.bodyUsed = false;
    this._body = env.body == null ? "" : String(env.body);
  }
  Response.prototype.text = function () { this.bodyUsed = true; return Promise.resolve(this._body); };
  Response.prototype.json = function () {
    var b = this._body; this.bodyUsed = true;
    return new Promise(function (res, rej) { try { res(JSON.parse(b)); } catch (e) { rej(e); } });
  };
  Response.prototype.arrayBuffer = function () { this.bodyUsed = true; return Promise.resolve(new TextEncoder().encode(this._body).buffer); };

  // --- FormData ---
  function FormData() { this._f = []; }
  FormData.prototype.append = function (name, value, filename) { this._f.push([String(name), value, filename]); };
  FormData.prototype.set = function (name, value, filename) { this.delete(name); this.append(name, value, filename); };
  FormData.prototype.get = function (name) { for (var i = 0; i < this._f.length; i++) if (this._f[i][0] === name) return this._f[i][1]; return null; };
  FormData.prototype.getAll = function (name) { return this._f.filter(function (e) { return e[0] === name; }).map(function (e) { return e[1]; }); };
  FormData.prototype.has = function (name) { return this.get(name) !== null; };
  FormData.prototype.delete = function (name) { this._f = this._f.filter(function (e) { return e[0] !== name; }); };
  FormData.prototype.forEach = function (fn, self) { this._f.forEach(function (e) { fn.call(self, e[1], e[0], this); }, this); };

  function encodeMultipart(fd) {
    var boundary = "----NocturnFormBoundary" + Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
    var body = "";
    fd._f.forEach(function (e) {
      var name = e[0], value = e[1], filename = e[2];
      body += "--" + boundary + "\r\n";
      if (filename != null) {
        body += 'Content-Disposition: form-data; name="' + name + '"; filename="' + filename + '"\r\n';
        body += "Content-Type: application/octet-stream\r\n\r\n";
      } else {
        body += 'Content-Disposition: form-data; name="' + name + '"\r\n\r\n';
      }
      body += (value instanceof Uint8Array ? bytesToBin(value) : String(value)) + "\r\n";
    });
    body += "--" + boundary + "--\r\n";
    return { body: body, contentType: "multipart/form-data; boundary=" + boundary };
  }

  // serializeBody maps a fetch body onto the {body, content_type} http.write takes.
  function serializeBody(body) {
    if (body == null) return { body: "", contentType: undefined };
    if (typeof body === "string") return { body: body, contentType: undefined };
    if (body instanceof URLSearchParams) return { body: body.toString(), contentType: "application/x-www-form-urlencoded;charset=UTF-8" };
    if (body instanceof FormData) return encodeMultipart(body);
    if (body instanceof Uint8Array) return { body: bytesToBin(body), contentType: undefined };
    if (typeof body === "object") return { body: JSON.stringify(body), contentType: "application/json" };
    return { body: String(body), contentType: undefined };
  }

  // --- fetch → nocturn.call (http.read / http.write) ---
  // NOTE: request headers other than Content-Type are NOT forwarded — the host
  // owns the credential channel and injects auth at the boundary (a guest may not
  // set Authorization/Cookie/etc.). A denied or failed action rejects with the
  // thrown error, matching WHATWG fetch's network-error rejection.
  function fetch(url, opts) {
    opts = opts || {};
    return new Promise(function (resolve, reject) {
      try {
        var method = String(opts.method || "GET").toUpperCase();
        var reqHeaders = new Headers(opts.headers || {});
        var out;
        if (method === "GET" || method === "HEAD") {
          out = g.nocturn.call("http.read", { url: String(url), method: method });
        } else {
          var ser = serializeBody(opts.body);
          var ct = reqHeaders.get("content-type") || ser.contentType || "application/json";
          out = g.nocturn.call("http.write", { url: String(url), method: method, body: ser.body, content_type: ct });
        }
        resolve(new Response(JSON.parse(out)));
      } catch (e) {
        reject(e instanceof Error ? e : new TypeError(String(e)));
      }
    });
  }

  // --- filesystem: nocturn.fs (clean async) + a node-ish fs compat shim ---
  function promisify(fn) {
    return function () {
      var args = arguments, self = this;
      return new Promise(function (res, rej) { try { res(fn.apply(self, args)); } catch (e) { rej(e); } });
    };
  }
  function toContent(data) { return data instanceof Uint8Array ? bytesToBin(data) : String(data); }

  var nfs = {
    readFile: function (path) { return g.nocturn.call("file.read", { path: String(path) }); },
    writeFile: function (path, data) { g.nocturn.call("file.write", { path: String(path), content: toContent(data) }); },
    list: function (path) { return JSON.parse(g.nocturn.call("file.list", { path: path == null ? "" : String(path) })); },
    stat: function (path) { return JSON.parse(g.nocturn.call("file.stat", { path: String(path) })); },
    remove: function (path) { g.nocturn.call("file.remove", { path: String(path) }); },
    // file.search returns a JSON array, but a truncated sweep appends a
    // "(truncated ...)" note after the JSON — split it off before parsing.
    search: function (pattern, path) {
      var raw = g.nocturn.call("file.search", { pattern: String(pattern), path: path == null ? "" : String(path) });
      var nl = raw.indexOf("\n");
      return JSON.parse(nl === -1 ? raw : raw.slice(0, nl));
    },
    move: function (from, to) { g.nocturn.call("file.move", { from: String(from), to: String(to) }); },
  };
  g.nocturn.fs = {
    readFile: promisify(nfs.readFile),
    writeFile: promisify(nfs.writeFile),
    list: promisify(nfs.list),
    stat: promisify(nfs.stat),
    remove: promisify(nfs.remove),
    search: promisify(nfs.search),
    move: promisify(nfs.move),
  };

  // --- ping / clock: thin sync wrappers over the host tools (ping is gated like
  // dns; time.now carries no authority). Both return the parsed JSON object. ---
  g.nocturn.ping = function (host) { return JSON.parse(g.nocturn.call("ping", { host: String(host) })); };
  g.nocturn.now = function () { return JSON.parse(g.nocturn.call("time.now", {})); };
  // notify(message[, title]) — proactively tell the user (fire-and-forget).
  g.nocturn.notify = function (message, title) {
    var args = { message: String(message) };
    if (title != null) args.title = String(title);
    return JSON.parse(g.nocturn.call("notify", args));
  };
  // remind(when, message[, title]) — schedule a notification at a future time.
  // when = RFC3339 timestamp or "in <duration>" (e.g. "in 2h").
  g.nocturn.remind = function (when, message, title) {
    var args = { when: String(when), message: String(message) };
    if (title != null) args.title = String(title);
    return JSON.parse(g.nocturn.call("remind", args));
  };
  // wake(seconds, note) — resume this session after a delay with `note` as the prompt.
  g.nocturn.wake = function (seconds, note) {
    return JSON.parse(g.nocturn.call("wake", { seconds: Number(seconds), note: String(note) }));
  };
  // resolve(host[, type]) — type ∈ A|AAAA|IP|MX|TXT|CNAME|NS|PTR|SRV (default A).
  g.nocturn.resolve = function (host, type) {
    var args = { host: String(host) };
    if (type != null) args.type = String(type);
    return JSON.parse(g.nocturn.call("dns.resolve", args));
  };

  var fs = {
    readFileSync: function (path) { return nfs.readFile(path); },
    writeFileSync: function (path, data) { nfs.writeFile(path, data); },
    readdirSync: function (path, opts) {
      var entries = nfs.list(path);
      if (opts && opts.withFileTypes) {
        return entries.map(function (e) {
          return { name: e.name, isDirectory: function () { return e.isDir; }, isFile: function () { return !e.isDir; } };
        });
      }
      return entries.map(function (e) { return e.name; });
    },
    statSync: function (path) {
      var s = nfs.stat(path);
      if (!s.exists) { var err = new Error("ENOENT: no such file or directory, stat '" + path + "'"); err.code = "ENOENT"; throw err; }
      return { isDirectory: function () { return s.isDir; }, isFile: function () { return !s.isDir; }, size: s.size };
    },
    existsSync: function (path) { try { return nfs.stat(path).exists; } catch (e) { return false; } },
    unlinkSync: function (path) { nfs.remove(path); },
    rmSync: function (path, opts) {
      if (opts && opts.recursive) throw new Error("nocturn: recursive rm is not supported");
      nfs.remove(path);
    },
    renameSync: function (from, to) { nfs.move(from, to); },
  };
  ["mkdirSync", "copyFileSync", "appendFileSync", "createReadStream", "createWriteStream", "openSync", "watch", "watchFile"].forEach(function (m) {
    fs[m] = function () { throw new Error("nocturn: fs." + m + " is not supported (use nocturn.fs or file.* tools)"); };
  });
  fs.promises = {
    readFile: promisify(fs.readFileSync), writeFile: promisify(fs.writeFileSync),
    readdir: promisify(fs.readdirSync), stat: promisify(fs.statSync),
    unlink: promisify(fs.unlinkSync), rm: promisify(fs.rmSync),
    rename: promisify(fs.renameSync),
  };

  function requireShim(m) {
    if (m === "fs" || m === "node:fs") return fs;
    if (m === "fs/promises" || m === "node:fs/promises") return fs.promises;
    if (m === "buffer" || m === "node:buffer") return { Buffer: Buffer };
    if (m === "util" || m === "node:util") return { TextEncoder: TextEncoder, TextDecoder: TextDecoder };
    throw new Error("nocturn: module '" + m + "' is not available");
  }

  // --- publish (no-clobber: a plugin or a future native wins) ---
  function def(name, value) { if (!g[name]) g[name] = value; }
  def("btoa", btoa);
  def("atob", atob);
  def("TextEncoder", TextEncoder);
  def("TextDecoder", TextDecoder);
  def("Buffer", Buffer);
  def("URLSearchParams", URLSearchParams);
  def("URL", URL);
  def("Headers", Headers);
  def("Response", Response);
  def("FormData", FormData);
  def("fetch", fetch);
  def("fs", fs);
  def("require", requireShim);
})(globalThis);
