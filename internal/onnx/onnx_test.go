package onnx_test

import (
	"math"
	"testing"

	"github.com/efuturetoday/nocturn/internal/onnx"
)

// assertClose reports whether two tensors agree to within float32 rounding.
func assertClose(t *testing.T, got *onnx.Tensor, wantShape []int, want []float32) {
	t.Helper()
	if len(got.Shape) != len(wantShape) {
		t.Fatalf("shape = %v, want %v", got.Shape, wantShape)
	}
	for i := range wantShape {
		if got.Shape[i] != wantShape[i] {
			t.Fatalf("shape = %v, want %v", got.Shape, wantShape)
		}
	}
	if len(got.Data) != len(want) {
		t.Fatalf("got %d elements, want %d", len(got.Data), len(want))
	}
	for i := range want {
		if math.Abs(float64(got.Data[i]-want[i])) > 1e-5 {
			t.Errorf("element %d = %v, want %v", i, got.Data[i], want[i])
		}
	}
}

// graph assembles a one-node graph so an operator can be exercised without an ONNX file.
func graph(op string, attrs map[string]onnx.Attr, inputs []string, init map[string]*onnx.Tensor) *onnx.Graph {
	return &onnx.Graph{
		Nodes:   []onnx.Node{{Op: op, Name: "n", In: inputs, Out: []string{"y"}, Attr: attrs}},
		Init:    init,
		Inputs:  []string{inputs[0]},
		Outputs: []string{"y"},
	}
}

func TestConvIdentityKernel(t *testing.T) {
	// A 1×1 kernel of weight 2 with bias 1 must scale and shift every element.
	g := graph("Conv", map[string]onnx.Attr{
		"kernel_shape": {Ints: []int64{1, 1}},
		"strides":      {Ints: []int64{1, 1}},
		"pads":         {Ints: []int64{0, 0, 0, 0}},
	}, []string{"x", "w", "b"}, map[string]*onnx.Tensor{
		"w": {Shape: []int{1, 1, 1, 1}, Data: []float32{2}},
		"b": {Shape: []int{1}, Data: []float32{1}},
	})

	x := &onnx.Tensor{Shape: []int{1, 1, 2, 2}, Data: []float32{1, 2, 3, 4}}
	out, err := g.Run(map[string]*onnx.Tensor{"x": x})
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, out[0], []int{1, 1, 2, 2}, []float32{3, 5, 7, 9})
}

func TestConvPaddingAndStride(t *testing.T) {
	// A 3×3 kernel of ones over a 3×3 input, padded by one and strided by two, samples the four
	// corner windows. Each sums the 2×2 block of the input that overlaps it.
	//
	//   input   1 2 3      windows (padded with zeros):
	//           4 5 6        top-left  1+2+4+5 = 12    top-right  2+3+5+6 = 16
	//           7 8 9        bot-left  4+5+7+8 = 24    bot-right  5+6+8+9 = 28
	g := graph("Conv", map[string]onnx.Attr{
		"kernel_shape": {Ints: []int64{3, 3}},
		"strides":      {Ints: []int64{2, 2}},
		"pads":         {Ints: []int64{1, 1, 1, 1}},
	}, []string{"x", "w"}, map[string]*onnx.Tensor{
		"w": {Shape: []int{1, 1, 3, 3}, Data: []float32{1, 1, 1, 1, 1, 1, 1, 1, 1}},
	})

	x := &onnx.Tensor{Shape: []int{1, 1, 3, 3}, Data: []float32{1, 2, 3, 4, 5, 6, 7, 8, 9}}
	out, err := g.Run(map[string]*onnx.Tensor{"x": x})
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, out[0], []int{1, 1, 2, 2}, []float32{12, 16, 24, 28})
}

func TestConvMultiChannel(t *testing.T) {
	// Two input channels, two filters: the first sums both channels, the second differences them.
	g := graph("Conv", map[string]onnx.Attr{
		"kernel_shape": {Ints: []int64{1, 1}},
		"strides":      {Ints: []int64{1, 1}},
		"pads":         {Ints: []int64{0, 0, 0, 0}},
	}, []string{"x", "w"}, map[string]*onnx.Tensor{
		"w": {Shape: []int{2, 2, 1, 1}, Data: []float32{1, 1, 1, -1}},
	})

	x := &onnx.Tensor{Shape: []int{1, 2, 1, 2}, Data: []float32{1, 2 /* channel 0 */, 10, 20 /* channel 1 */}}
	out, err := g.Run(map[string]*onnx.Tensor{"x": x})
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, out[0], []int{1, 2, 1, 2}, []float32{11, 22, -9, -18})
}

func TestGemmTransposedWeight(t *testing.T) {
	// y = x·Wᵀ + b, the layout every exported linear layer uses.
	g := graph("Gemm", map[string]onnx.Attr{"transB": {Int: 1}},
		[]string{"x", "w", "b"}, map[string]*onnx.Tensor{
			"w": {Shape: []int{2, 3}, Data: []float32{1, 0, 0, 0, 1, 1}},
			"b": {Shape: []int{2}, Data: []float32{10, 20}},
		})

	x := &onnx.Tensor{Shape: []int{1, 3}, Data: []float32{2, 3, 4}}
	out, err := g.Run(map[string]*onnx.Tensor{"x": x})
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, out[0], []int{1, 2}, []float32{12, 27})
}

func TestSubBroadcastsAcrossTime(t *testing.T) {
	// Centring a time series on its own per-row mean — what statistics pooling does before it
	// computes a standard deviation, and the only broadcast the head relies on.
	g := &onnx.Graph{
		Nodes: []onnx.Node{
			{Op: "ReduceMean", Name: "mean", In: []string{"x"}, Out: []string{"m"},
				Attr: map[string]onnx.Attr{"axes": {Ints: []int64{-1}}}},
			{Op: "Sub", Name: "centre", In: []string{"x", "m"}, Out: []string{"y"},
				Attr: map[string]onnx.Attr{}},
		},
		Init: map[string]*onnx.Tensor{}, Inputs: []string{"x"}, Outputs: []string{"y"},
	}

	x := &onnx.Tensor{Shape: []int{1, 2, 3}, Data: []float32{1, 2, 3, 10, 20, 30}}
	out, err := g.Run(map[string]*onnx.Tensor{"x": x})
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, out[0], []int{1, 2, 3}, []float32{-1, 0, 1, -10, 0, 10})
}

func TestConcatAlongChannels(t *testing.T) {
	g := graph("Concat", map[string]onnx.Attr{"axis": {Int: 1}},
		[]string{"x", "w"}, map[string]*onnx.Tensor{
			"w": {Shape: []int{2, 1}, Data: []float32{7, 8}},
		})

	x := &onnx.Tensor{Shape: []int{2, 2}, Data: []float32{1, 2, 3, 4}}
	out, err := g.Run(map[string]*onnx.Tensor{"x": x})
	if err != nil {
		t.Fatal(err)
	}
	// Row-major: row 0 is 1,2 then 7; row 1 is 3,4 then 8.
	assertClose(t, out[0], []int{2, 3}, []float32{1, 2, 7, 3, 4, 8})
}

func TestTransposeMovesData(t *testing.T) {
	g := graph("Transpose", map[string]onnx.Attr{"perm": {Ints: []int64{0, 2, 1}}},
		[]string{"x"}, map[string]*onnx.Tensor{})

	x := &onnx.Tensor{Shape: []int{1, 2, 3}, Data: []float32{1, 2, 3, 4, 5, 6}}
	out, err := g.Run(map[string]*onnx.Tensor{"x": x})
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, out[0], []int{1, 3, 2}, []float32{1, 4, 2, 5, 3, 6})
}

func TestUnsupportedOperatorIsAnError(t *testing.T) {
	// The package must refuse what it cannot compute; a silently wrong embedding is far worse
	// than a failed one, because nothing downstream can tell it apart from a genuine mismatch.
	g := graph("LSTM", map[string]onnx.Attr{}, []string{"x"}, map[string]*onnx.Tensor{})
	if _, err := g.Run(map[string]*onnx.Tensor{"x": onnx.NewTensor(1)}); err == nil {
		t.Fatal("running an unsupported operator returned no error")
	}
}

func TestMissingInputIsAnError(t *testing.T) {
	g := graph("Relu", map[string]onnx.Attr{}, []string{"x"}, map[string]*onnx.Tensor{})
	if _, err := g.Run(map[string]*onnx.Tensor{}); err == nil {
		t.Fatal("running without the graph input returned no error")
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	if _, err := onnx.Read([]byte("this is not a protobuf")); err == nil {
		t.Fatal("parsing garbage returned no error")
	}
}
