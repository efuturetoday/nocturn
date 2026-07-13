;; log_probe — the smallest possible guest that uses ONE granted window.
;;
;; It imports exactly one host function, nocturn.log(ptr, len), and nothing
;; else. No WASI, no network, no files. It places a message in its own linear
;; memory and hands the host two numbers — where the bytes are (ptr) and how
;; many (len). That (ptr, len) pair IS the memory ABI.
;;
;; Build: wat2wasm log_probe.wat -o ../logprobe.wasm
(module
  ;; The one window the host grants. WASM can only pass numbers, so text is
  ;; passed as (ptr, len) into this module's linear memory.
  (import "nocturn" "log" (func $log (param i32 i32)))

  ;; Our own linear memory (1 page = 64 KiB), exported so the host can read it.
  (memory (export "memory") 1)

  ;; The message bytes, written at offset 0. Length is 20 bytes.
  (data (i32.const 0) "hello from the guest")

  ;; Entry point: tell the host "read 20 bytes starting at offset 0".
  (func (export "_start")
    (call $log (i32.const 0) (i32.const 20))))
