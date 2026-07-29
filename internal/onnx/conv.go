package onnx

import "fmt"

// conv2d computes an NCHW convolution as im2col followed by a single matrix product: each output
// position's receptive field becomes one column of a [C·kh·kw, outH·outW] matrix, and the weights
// already are a [M, C·kh·kw] matrix, so the whole layer reduces to one call into the blocked GEMM.
//
// Only what a speaker-embedding ResNet issues is handled: batch one, one group, no dilation.
func conv2d(x, w, b *Tensor, stride, pad []int) (*Tensor, error) {
	if len(x.Shape) != 4 || x.Shape[0] != 1 {
		return nil, fmt.Errorf("Conv wants a [1,C,H,W] input, got %v", x.Shape)
	}
	if len(w.Shape) != 4 {
		return nil, fmt.Errorf("Conv wants a [M,C,kh,kw] weight, got %v", w.Shape)
	}
	channels, height, width := x.Shape[1], x.Shape[2], x.Shape[3]
	filters, kh, kw := w.Shape[0], w.Shape[2], w.Shape[3]
	if w.Shape[1] != channels {
		return nil, fmt.Errorf("Conv input has %d channels, weight expects %d", channels, w.Shape[1])
	}
	sh, sw := stride[0], stride[1]
	padTop, padLeft, padBottom, padRight := pad[0], pad[1], pad[2], pad[3]

	outH := (height+padTop+padBottom-kh)/sh + 1
	outW := (width+padLeft+padRight-kw)/sw + 1
	if outH <= 0 || outW <= 0 {
		return nil, fmt.Errorf("Conv on %v with kernel %dx%d yields an empty output", x.Shape, kh, kw)
	}
	patch := channels * kh * kw
	positions := outH * outW

	// Zero-initialised, so padded positions simply stay zero and need no branch in the copy.
	col := make([]float32, patch*positions)
	for c := range channels {
		for ki := range kh {
			for kj := range kw {
				dst := col[((c*kh+ki)*kw+kj)*positions:]
				for oh := range outH {
					ih := oh*sh - padTop + ki
					if ih < 0 || ih >= height {
						continue
					}
					src := x.Data[(c*height+ih)*width:]
					base := oh * outW
					for ow := range outW {
						if iw := ow*sw - padLeft + kj; iw >= 0 && iw < width {
							dst[base+ow] = src[iw]
						}
					}
				}
			}
		}
	}

	out := NewTensor(1, filters, outH, outW)
	gemm(filters, patch, positions, w.Data, col, out.Data)

	if b != nil {
		if len(b.Data) != filters {
			return nil, fmt.Errorf("Conv bias has %d entries, expected %d", len(b.Data), filters)
		}
		for f := range filters {
			bias := b.Data[f]
			row := out.Data[f*positions : (f+1)*positions]
			for i := range row {
				row[i] += bias
			}
		}
	}
	return out, nil
}
