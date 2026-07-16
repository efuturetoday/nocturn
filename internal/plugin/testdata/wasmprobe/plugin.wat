;; plugin.wat — a minimal wasm32-wasi plugin guest that exercises the runWASM
;; contract end to end. The host feeds it one request on stdin
;; (JSON {"tool":...,"args":...}); it passes those bytes verbatim to the host
;; import nocturn.call over the standard packed-ptr ABI, then writes the returned
;; bytes to stdout. It exports memory + a bump malloc/free so the host can
;; allocate the response inside it. This is the smallest possible plugin.wasm: it
;; self-dispatches by forwarding, proving the {tool,args} stdin → gate → registry
;; → stdout round-trip a real (Rust/Go→wasm) plugin would drive.
;;
;; Build: wat2wasm plugin.wat -o plugin.wasm
(module
  (import "wasi_snapshot_preview1" "fd_read"
    (func $fd_read (param i32 i32 i32 i32) (result i32)))
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))
  (import "nocturn" "call"
    (func $call (param i32 i32) (result i64)))

  (memory (export "memory") 4)

  ;; bump allocator: hands out memory past the fixed scratch/input region
  (global $bump (mut i32) (i32.const 131072))
  (func (export "malloc") (param $n i32) (result i32)
    (local $p i32)
    (local.set $p (global.get $bump))
    (global.set $bump (i32.add (global.get $bump) (local.get $n)))
    (local.get $p))
  (func (export "free") (param i32))

  (func (export "_start")
    (local $nread i32)
    (local $packed i64)
    (local $addr i32)
    (local $len i32)
    ;; read stdin: iovec {ptr=1024, len=65536} at [0], nread at [8]
    (i32.store (i32.const 0) (i32.const 1024))
    (i32.store (i32.const 4) (i32.const 65536))
    (drop (call $fd_read (i32.const 0) (i32.const 0) (i32.const 1) (i32.const 8)))
    (local.set $nread (i32.load (i32.const 8)))
    ;; host call(reqPtr=1024, reqLen=nread) -> packed (addr<<32 | size)
    (local.set $packed (call $call (i32.const 1024) (local.get $nread)))
    (local.set $addr (i32.wrap_i64 (i64.shr_u (local.get $packed) (i64.const 32))))
    (local.set $len (i32.wrap_i64 (local.get $packed)))
    ;; write stdout: iovec {ptr=addr, len=len} at [16], nwritten at [24]
    (i32.store (i32.const 16) (local.get $addr))
    (i32.store (i32.const 20) (local.get $len))
    (drop (call $fd_write (i32.const 1) (i32.const 16) (i32.const 1) (i32.const 24)))))
