;; mutate.wat — proves the trampoline copies the transient wazero Memory.Read
;; view OUT before the guest can mutate the backing region. The guest reads
;; stdin into 1024, hands (1024,n) to the host function nocturn.echo (which the
;; test retains), then OVERWRITES that same region with 0xFF and exits. If the
;; host kept the raw view instead of copying, the retained bytes would read back
;; as 0xFF; with the immediate copy they stay equal to the input.
;; Build: wat2wasm mutate.wat -o mutate.wasm
(module
  (import "wasi_snapshot_preview1" "fd_read"
    (func $fd_read (param i32 i32 i32 i32) (result i32)))
  (import "nocturn" "echo"
    (func $echo (param i32 i32) (result i64)))

  (memory (export "memory") 4)

  ;; bump allocator so the host can malloc its response inside the guest
  (global $bump (mut i32) (i32.const 131072))
  (func (export "malloc") (param $n i32) (result i32)
    (local $p i32)
    (local.set $p (global.get $bump))
    (global.set $bump (i32.add (global.get $bump) (local.get $n)))
    (local.get $p))
  (func (export "free") (param i32))

  (func (export "_start")
    (local $nread i32)
    (local $i i32)
    ;; read stdin: iovec {ptr=1024, len=65536} at [0], nread at [8]
    (i32.store (i32.const 0) (i32.const 1024))
    (i32.store (i32.const 4) (i32.const 65536))
    (drop (call $fd_read (i32.const 0) (i32.const 0) (i32.const 1) (i32.const 8)))
    (local.set $nread (i32.load (i32.const 8)))
    ;; hand the input region to the host, which retains it; drop the packed ptr
    (drop (call $echo (i32.const 1024) (local.get $nread)))
    ;; clobber the SAME region with 0xFF — a retained raw view would now change
    (local.set $i (i32.const 0))
    (block $done
      (loop $l
        (br_if $done (i32.ge_u (local.get $i) (local.get $nread)))
        (i32.store8 (i32.add (i32.const 1024) (local.get $i)) (i32.const 0xFF))
        (local.set $i (i32.add (local.get $i) (i32.const 1)))
        (br $l)))))
