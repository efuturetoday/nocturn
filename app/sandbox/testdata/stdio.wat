;; stdio.wat — pipes stdin straight to BOTH stdout (fd 1) and stderr (fd 2),
;; proving WASI stdio is wired in all three directions. No host imports.
;; Build: wat2wasm stdio.wat -o stdio.wasm
(module
  (import "wasi_snapshot_preview1" "fd_read"
    (func $fd_read (param i32 i32 i32 i32) (result i32)))
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))

  (memory (export "memory") 2)

  (func (export "_start")
    (local $nread i32)
    ;; read stdin: iovec {ptr=1024, len=65536} at [0], nread at [8]
    (i32.store (i32.const 0) (i32.const 1024))
    (i32.store (i32.const 4) (i32.const 65536))
    (drop (call $fd_read (i32.const 0) (i32.const 0) (i32.const 1) (i32.const 8)))
    (local.set $nread (i32.load (i32.const 8)))
    ;; iovec {ptr=1024, len=nread} at [16] for both writes
    (i32.store (i32.const 16) (i32.const 1024))
    (i32.store (i32.const 20) (local.get $nread))
    (drop (call $fd_write (i32.const 1) (i32.const 16) (i32.const 1) (i32.const 24)))
    (drop (call $fd_write (i32.const 2) (i32.const 16) (i32.const 1) (i32.const 24)))))
