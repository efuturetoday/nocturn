;; loop.wat — a guest that never terminates, to prove the wall-clock deadline
;; traps a runaway guest (the wazero #422 guarantee).
;; Build: wat2wasm loop.wat -o loop.wasm
(module
  (memory (export "memory") 1)
  (func (export "_start")
    (loop $l (br $l))))
