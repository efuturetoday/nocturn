;; nomalloc.wat — a memory-only reactor guest with NO exported malloc and no
;; _start. Used white-box to prove writeToGuest returns 0 when the guest exports
;; no usable allocator (and, with an empty payload, before it even looks).
;; Build: wat2wasm nomalloc.wat -o nomalloc.wasm
(module
  (memory (export "memory") 1))
