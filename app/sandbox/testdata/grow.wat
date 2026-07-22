;; grow.wat — proves the memory cap (EngineConfig.MaxPages) is enforced: the
;; guest tries to memory.grow past the cap; when the grow is refused it stays at
;; one page, and a store into the page it wanted traps (out-of-bounds) — the host
;; is never allowed to actually allocate the memory. Under a generous cap the grow
;; succeeds and the store lands, so this guest only traps when the cap bites.
;; Build: wat2wasm grow.wat -o grow.wasm
(module
  (memory (export "memory") 1)
  (func (export "_start")
    ;; ask for 100 more pages; capped runtimes return -1 and do not grow
    (drop (memory.grow (i32.const 100)))
    ;; touch page 100 (offset 100*65536): out of bounds unless the grow happened
    (i32.store (i32.const 6553600) (i32.const 1))))
