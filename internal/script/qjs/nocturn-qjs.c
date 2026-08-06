/*
 * nocturn-qjs.c — QuickJS (quickjs-ng) as a WASI *command* guest for Nocturn's
 * sandbox (internal/sandbox). It is the real interpreter that sits on shell 0.
 *
 * Contract with the host:
 *   - stdin  : the JavaScript source to evaluate.
 *   - stdout : whatever the script prints (console.log / print).
 *   - stderr : an uncaught exception's message + stack; exit code 1.
 *   - imports: exactly ONE host function, nocturn.call — the generic tool gate.
 *              (WASI itself is the other import module; see crypto below.)
 *   - exports: malloc/free so the host can allocate the gate's response inside
 *              guest memory (the standard packed-ptr ABI in sandbox.go).
 *
 * The script reaches every tool through nocturn.call(tool, args): it
 * serialises {tool, args} to JSON, hands it to the host gate, and the host
 * dispatches to the same brain.Tool registry the model uses (Guard.Authorize +
 * HITL). A pure-compute script never calls the gate and performs no action.
 *
 * Build (needs wasi-sdk + a quickjs-ng checkout): see build.sh in this dir.
 */
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "quickjs.h"

/* The single host import: the generic tool gate. req is JSON
 * {"tool":"..","args":..}; the return is the packed (addr<<32 | len) of a
 * response the host allocated in guest memory via our exported malloc. */
__attribute__((import_module("nocturn"), import_name("call")))
extern uint64_t nocturn_host_call(uint32_t ptr, uint32_t len);

static void write_all(int fd, const char *s, size_t len) {
    while (len) {
        ssize_t w = write(fd, s, len);
        if (w <= 0)
            break;
        s += (size_t)w;
        len -= (size_t)w;
    }
}

/* print(...) / console.log(...) — space-joined args + newline to stdout. */
static JSValue js_print(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv) {
    (void)this_val;
    for (int i = 0; i < argc; i++) {
        if (i)
            write_all(1, " ", 1);
        size_t len;
        const char *s = JS_ToCStringLen(ctx, &len, argv[i]);
        if (!s)
            return JS_EXCEPTION;
        write_all(1, s, len);
        JS_FreeCString(ctx, s);
    }
    write_all(1, "\n", 1);
    return JS_UNDEFINED;
}

/* nocturn.call(tool, args) — dispatch an action through the host gate. An
 * "error: ..." response (a denied/failed action) becomes a thrown JS error the
 * script can try/catch; the host never crashes. */
static JSValue js_nocturn_call(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv) {
    (void)this_val;
    if (argc < 1)
        return JS_ThrowTypeError(ctx, "nocturn.call(tool, args): tool name required");

    JSValue req = JS_NewObject(ctx);
    JS_SetPropertyStr(ctx, req, "tool", JS_DupValue(ctx, argv[0]));
    JS_SetPropertyStr(ctx, req, "args", argc > 1 ? JS_DupValue(ctx, argv[1]) : JS_NewObject(ctx));
    JSValue json = JS_JSONStringify(ctx, req, JS_UNDEFINED, JS_UNDEFINED);
    JS_FreeValue(ctx, req);
    if (JS_IsException(json))
        return json;

    size_t len;
    const char *s = JS_ToCStringLen(ctx, &len, json);
    JS_FreeValue(ctx, json);
    if (!s)
        return JS_EXCEPTION;

    uint64_t packed = nocturn_host_call((uint32_t)(uintptr_t)s, (uint32_t)len);
    JS_FreeCString(ctx, s);

    uint32_t addr = (uint32_t)(packed >> 32);
    uint32_t rlen = (uint32_t)(packed & 0xffffffffu);
    if (addr == 0 || rlen == 0)
        return JS_NewString(ctx, "");

    char *resp = (char *)(uintptr_t)addr;
    JSValue out;
    if (rlen >= 7 && memcmp(resp, "error: ", 7) == 0) {
        /* strip the host's "error: " prefix; surface the rest as an exception */
        out = JS_ThrowTypeError(ctx, "%.*s", (int)(rlen - 7), resp + 7);
    } else {
        out = JS_NewStringLen(ctx, resp, rlen);
    }
    free(resp); /* host allocated it via our malloc; guest owns and frees it */
    return out;
}

/* crypto.getRandomValues(view) — fill an integer TypedArray with entropy from
 * the host, and return it, per the Web Crypto shape scripts already expect.
 *
 * quickjs-ng ships no WebCrypto, and its Math.random is a PRNG seeded from the
 * wall clock — under a sandbox whose clock is worth little as a seed, and never
 * suitable for anything a third party may guess at. Multipart boundaries in the
 * prelude are the concrete case: a predictable boundary lets a crafted field
 * value close the part early and forge the rest of the body.
 *
 * getentropy() is wasi-libc's thin wrapper over the WASI random_get import, so
 * this reaches the host's crypto/rand and adds no host function of our own —
 * the "exactly one import" contract above is intact.
 *
 * The 65536-byte cap and the QuotaExceededError are the Web Crypto spec's, kept
 * so a script written against a browser behaves the same here. getentropy()
 * itself refuses more than 256 bytes at a time, hence the loop. */
static JSValue js_get_random_values(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv) {
    (void)this_val;
    if (argc < 1)
        return JS_ThrowTypeError(ctx, "crypto.getRandomValues(view): a typed array is required");

    size_t off = 0, len = 0, bpe = 0;
    JSValue buf = JS_GetTypedArrayBuffer(ctx, argv[0], &off, &len, &bpe);
    if (JS_IsException(buf))
        return JS_ThrowTypeError(ctx, "crypto.getRandomValues(view): a typed array is required");

    size_t total = 0;
    uint8_t *mem = JS_GetArrayBuffer(ctx, &total, buf);
    JS_FreeValue(ctx, buf);
    if (!mem)
        return JS_ThrowTypeError(ctx, "crypto.getRandomValues(view): the buffer is detached");
    if (len > 65536)
        return JS_ThrowRangeError(ctx, "crypto.getRandomValues(view): at most 65536 bytes");

    uint8_t *p = mem + off;
    for (size_t done = 0; done < len;) {
        size_t n = len - done;
        if (n > 256)
            n = 256;
        if (getentropy(p + done, n) != 0)
            return JS_ThrowInternalError(ctx, "crypto.getRandomValues: the host refused entropy");
        done += n;
    }
    return JS_DupValue(ctx, argv[0]);
}

/* Read all of a fd into a NUL-terminated heap buffer; *out_len excludes the NUL. */
static char *slurp(int fd, size_t *out_len) {
    size_t cap = 1 << 16, n = 0;
    char *buf = malloc(cap);
    if (!buf)
        return NULL;
    for (;;) {
        if (n + 65536 + 1 > cap) {
            cap *= 2;
            char *t = realloc(buf, cap);
            if (!t) {
                free(buf);
                return NULL;
            }
            buf = t;
        }
        ssize_t r = read(fd, buf + n, 65536);
        if (r < 0) {
            free(buf);
            return NULL;
        }
        if (r == 0)
            break;
        n += (size_t)r;
    }
    buf[n] = '\0';
    *out_len = n;
    return buf;
}

/* Print an error value (an exception or a promise-rejection reason) to stderr:
 * its message plus, if present, its .stack. */
static void report_error_value(JSContext *ctx, JSValueConst v) {
    const char *msg = JS_ToCString(ctx, v);
    if (msg) {
        write_all(2, msg, strlen(msg));
        write_all(2, "\n", 1);
        JS_FreeCString(ctx, msg);
    }
    JSValue stack = JS_GetPropertyStr(ctx, v, "stack");
    if (!JS_IsUndefined(stack)) {
        const char *st = JS_ToCString(ctx, stack);
        if (st) {
            write_all(2, st, strlen(st));
            write_all(2, "\n", 1);
            JS_FreeCString(ctx, st);
        }
    }
    JS_FreeValue(ctx, stack);
}

static void report_exception(JSContext *ctx) {
    JSValue exc = JS_GetException(ctx);
    report_error_value(ctx, exc);
    JS_FreeValue(ctx, exc);
}

int main(void) {
    size_t srclen = 0;
    char *src = slurp(0, &srclen);
    if (!src)
        return 1;

    JSRuntime *rt = JS_NewRuntime();
    if (!rt) {
        free(src);
        return 1;
    }
    JSContext *ctx = JS_NewContext(rt);
    if (!ctx) {
        JS_FreeRuntime(rt);
        free(src);
        return 1;
    }

    /* Globals: nocturn.call (the gate), console.log/console.error, print,
     * crypto.getRandomValues. */
    JSValue global = JS_GetGlobalObject(ctx);
    JSValue nocturn = JS_NewObject(ctx);
    JS_SetPropertyStr(ctx, nocturn, "call", JS_NewCFunction(ctx, js_nocturn_call, "call", 2));
    JS_SetPropertyStr(ctx, global, "nocturn", nocturn);
    JSValue console = JS_NewObject(ctx);
    JS_SetPropertyStr(ctx, console, "log", JS_NewCFunction(ctx, js_print, "log", 1));
    JS_SetPropertyStr(ctx, console, "error", JS_NewCFunction(ctx, js_print, "error", 1));
    JS_SetPropertyStr(ctx, global, "console", console);
    JS_SetPropertyStr(ctx, global, "print", JS_NewCFunction(ctx, js_print, "print", 1));
    JSValue crypto = JS_NewObject(ctx);
    JS_SetPropertyStr(ctx, crypto, "getRandomValues",
                      JS_NewCFunction(ctx, js_get_random_values, "getRandomValues", 1));
    JS_SetPropertyStr(ctx, global, "crypto", crypto);
    JS_FreeValue(ctx, global);

    int status = 0;
    /* Evaluate as async global code: JS_EVAL_FLAG_ASYNC permits top-level await
     * (models naturally write `await nocturn.call(...)`) and returns a promise
     * for the whole script. nocturn.call itself is synchronous, but awaiting it
     * is harmless. */
    JSValue ret = JS_Eval(ctx, src, srclen, "<stdin>",
                          JS_EVAL_TYPE_GLOBAL | JS_EVAL_FLAG_ASYNC);
    if (JS_IsException(ret)) {
        report_exception(ctx); /* a syntax/compile error */
        status = 1;
    } else {
        /* Drive the microtask/job queue to completion, so awaited continuations,
         * .then() callbacks and async functions actually run — nothing pumps
         * them otherwise. */
        for (;;) {
            JSContext *jc;
            int r = JS_ExecutePendingJob(rt, &jc);
            if (r < 0) {
                report_exception(jc);
                status = 1;
                break;
            }
            if (r == 0)
                break;
        }
        /* Surface a rejected (or never-settled) top-level promise as an error. */
        if (status == 0 && JS_IsPromise(ret)) {
            switch (JS_PromiseState(ctx, ret)) {
            case JS_PROMISE_REJECTED: {
                JSValue reason = JS_PromiseResult(ctx, ret);
                report_error_value(ctx, reason);
                JS_FreeValue(ctx, reason);
                status = 1;
                break;
            }
            case JS_PROMISE_PENDING: {
                static const char *m = "nocturn-qjs: script did not settle (awaited a promise that never resolved)";
                write_all(2, m, strlen(m));
                write_all(2, "\n", 1);
                status = 1;
                break;
            }
            default:
                break;
            }
        }
    }
    JS_FreeValue(ctx, ret);

    JS_FreeContext(ctx);
    JS_FreeRuntime(rt);
    free(src);
    return status;
}
