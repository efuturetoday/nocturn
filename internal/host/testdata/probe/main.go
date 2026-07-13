// Command probe is an untrusted test guest. It attempts to touch the outside
// world by writing to stdout. Under wasip1 that requires the host to grant
// WASI. With zero authority granted, the guest cannot even instantiate — which
// is exactly what the host_test.go zero-authority test asserts.
//
// Built with: GOOS=wasip1 GOARCH=wasm go build -o ../probe.wasm ./
package main

import "os"

func main() {
	_, _ = os.Stdout.WriteString("hello from the guest\n")
}
