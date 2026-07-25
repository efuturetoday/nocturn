;; fsprobe.wat — proves the workspace is the ONLY filesystem: the guest can
;; create a file inside /work (preopen fd 3) but cannot escape it. It writes a
;; single result byte to stdout: bit0 = create-in-/work succeeded, bit1 = escape
;; open succeeded (must be 0). Expected byte: 0x01.
;;
;; wazero ignores the WASI rights args (rights were removed from WASI), except
;; RIGHT_FD_WRITE which opens O_RDWR — so we pass -1 (all bits) safely.
;; Build: wat2wasm fsprobe.wat -o fsprobe.wasm
(module
  (import "wasi_snapshot_preview1" "path_open"
    (func $path_open (param i32 i32 i32 i32 i32 i64 i64 i32 i32) (result i32)))
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))

  (memory (export "memory") 1)
  (data (i32.const 100) "probe.txt")             ;; 9 bytes, inside /work
  (data (i32.const 200) "../../../etc/hostname") ;; 21 bytes, escape attempt

  (func (export "_start")
    (local $wok i32)
    (local $eok i32)
    ;; create /work/probe.txt (oflags O_CREAT|O_TRUNC = 9), result fd at [0]
    (local.set $wok (i32.eqz
      (call $path_open (i32.const 3) (i32.const 0) (i32.const 100) (i32.const 9)
        (i32.const 9) (i64.const -1) (i64.const -1) (i32.const 0) (i32.const 0))))
    ;; try to open a path that escapes the workspace (oflags 0 = read), fd at [8]
    (local.set $eok (i32.eqz
      (call $path_open (i32.const 3) (i32.const 0) (i32.const 200) (i32.const 21)
        (i32.const 0) (i64.const -1) (i64.const -1) (i32.const 0) (i32.const 8))))
    ;; result byte at [50] = wok | (eok<<1); write it to stdout
    (i32.store8 (i32.const 50)
      (i32.or (local.get $wok) (i32.shl (local.get $eok) (i32.const 1))))
    (i32.store (i32.const 60) (i32.const 50))
    (i32.store (i32.const 64) (i32.const 1))
    (drop (call $fd_write (i32.const 1) (i32.const 60) (i32.const 1) (i32.const 70)))))
