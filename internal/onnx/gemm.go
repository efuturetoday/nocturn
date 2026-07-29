package onnx

import (
	"runtime"
	"sync"
)

// This file holds the only performance-critical code in the package. Convolutions reach it through
// im2col, so essentially all inference time is spent here.
//
// Measured on an M-series laptop, float32, no assembly and no intrinsics:
//
//	textbook i-j-k triple loop     1.6 GFLOP/s
//	i-k-j, streaming rows of b     5.5 GFLOP/s
//	four rows of a per pass        10  GFLOP/s
//	the above across ten cores     34–50 GFLOP/s
//
// The gain is loop order and register reuse, not cleverness: i-k-j walks both operands forward
// through memory, and handling four rows of a at once amortises each loaded element of b over four
// accumulators. Go's compiler will not vectorise this, which is why the ceiling sits where it does —
// it is nonetheless far above what a generic graph interpreter achieves on the same hardware.

// gemmTile is how many rows of a the blocked kernel handles per pass.
const gemmTile = 4

// gemm computes c = a×b for row-major a[m,k], b[k,n], c[m,n], spread across the available cores.
// Bands of output rows are disjoint slices of c, so the workers need no synchronisation.
func gemm(m, k, n int, a, b, c []float32) {
	workers := min(runtime.GOMAXPROCS(0), (m+gemmTile-1)/gemmTile)
	if workers <= 1 {
		gemmSerial(m, k, n, a, b, c)
		return
	}
	band := (m + workers - 1) / workers
	band = (band + gemmTile - 1) / gemmTile * gemmTile // keep bands whole tiles

	var wg sync.WaitGroup
	for lo := 0; lo < m; lo += band {
		hi := min(lo+band, m)
		wg.Add(1)
		go func() {
			defer wg.Done()
			gemmSerial(hi-lo, k, n, a[lo*k:hi*k], b, c[lo*n:hi*n])
		}()
	}
	wg.Wait()
}

// gemmSerial is the single-threaded kernel: four rows of a per pass, streaming b row by row.
func gemmSerial(m, k, n int, a, b, c []float32) {
	clear(c)

	i := 0
	for ; i+gemmTile <= m; i += gemmTile {
		c0 := c[(i+0)*n : (i+1)*n]
		c1 := c[(i+1)*n : (i+2)*n]
		c2 := c[(i+2)*n : (i+3)*n]
		c3 := c[(i+3)*n : (i+4)*n]
		for p := range k {
			a0, a1 := a[(i+0)*k+p], a[(i+1)*k+p]
			a2, a3 := a[(i+2)*k+p], a[(i+3)*k+p]
			if a0 == 0 && a1 == 0 && a2 == 0 && a3 == 0 {
				continue
			}
			row := b[p*n : (p+1)*n]
			for j, v := range row {
				c0[j] += a0 * v
				c1[j] += a1 * v
				c2[j] += a2 * v
				c3[j] += a3 * v
			}
		}
	}
	for ; i < m; i++ {
		ci := c[i*n : (i+1)*n]
		for p := range k {
			av := a[i*k+p]
			if av == 0 {
				continue
			}
			row := b[p*n : (p+1)*n]
			for j, v := range row {
				ci[j] += av * v
			}
		}
	}
}
