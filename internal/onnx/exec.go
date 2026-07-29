package onnx

import (
	"fmt"
	"math"
)

// Run executes the graph and returns its outputs, in the order Graph.Outputs lists them.
//
// Nodes run in file order. The ONNX specification requires exporters to emit nodes topologically,
// so a value is always produced before it is read; a file that violates that fails loudly with a
// missing-input error rather than computing something wrong.
//
// Run holds no state between calls and mutates neither the graph nor the inputs, so one parsed
// Graph may be shared by concurrent callers.
//
// Operators that only reinterpret a shape return tensors sharing storage with their input, so a
// returned tensor may alias an input or a weight. Callers that intend to modify a result must copy
// it first — writing through an alias would corrupt the model.
func (g *Graph) Run(inputs map[string]*Tensor) ([]*Tensor, error) {
	for _, name := range g.Inputs {
		if inputs[name] == nil {
			return nil, fmt.Errorf("onnx: graph input %q was not supplied", name)
		}
	}

	values := make(map[string]*Tensor, len(g.Init)+len(g.Nodes))
	for name, t := range g.Init {
		values[name] = t
	}
	for name, t := range inputs {
		values[name] = t
	}

	for i := range g.Nodes {
		n := &g.Nodes[i]
		in := make([]*Tensor, len(n.In))
		for j, name := range n.In {
			if name == "" { // an omitted optional input, e.g. a convolution without bias
				continue
			}
			t, ok := values[name]
			if !ok {
				return nil, fmt.Errorf("onnx: node %d (%s %q): input %q is not defined", i, n.Op, n.Name, name)
			}
			in[j] = t
		}
		out, err := apply(n, in)
		if err != nil {
			return nil, fmt.Errorf("onnx: node %d (%s %q): %w", i, n.Op, n.Name, err)
		}
		values[n.Out[0]] = out
	}

	outs := make([]*Tensor, len(g.Outputs))
	for i, name := range g.Outputs {
		t, ok := values[name]
		if !ok {
			return nil, fmt.Errorf("onnx: graph output %q was never produced", name)
		}
		outs[i] = t
	}
	return outs, nil
}

// apply computes one node. Every unsupported operator, and every unsupported variant of a supported
// one, returns an error — this package answers correctly or not at all.
func apply(n *Node, in []*Tensor) (*Tensor, error) {
	switch n.Op {
	case "Constant":
		a, ok := n.Attr["value"]
		if !ok || a.Tensor == nil {
			return nil, fmt.Errorf("Constant carries no tensor value")
		}
		return a.Tensor, nil

	case "Relu":
		return unary(in[0], func(v float32) float32 { return max(v, 0) }), nil
	case "Sqrt":
		return unary(in[0], func(v float32) float32 { return float32(math.Sqrt(float64(v))) }), nil
	case "Sigmoid":
		return unary(in[0], func(v float32) float32 {
			return float32(1 / (1 + math.Exp(-float64(v))))
		}), nil

	case "Add":
		return elementwise(in[0], in[1], func(x, y float32) float32 { return x + y })
	case "Sub":
		return elementwise(in[0], in[1], func(x, y float32) float32 { return x - y })
	case "Mul":
		return elementwise(in[0], in[1], func(x, y float32) float32 { return x * y })
	case "Div":
		return elementwise(in[0], in[1], func(x, y float32) float32 { return x / y })

	case "Cast":
		// Every tensor in this package is float32 already; the graphs only ever cast shape
		// arithmetic between int64 and float, which is a no-op here.
		return in[0], nil

	case "Shape":
		out := NewTensor(len(in[0].Shape))
		for i, d := range in[0].Shape {
			out.Data[i] = float32(d)
		}
		return out, nil

	case "Gather":
		return gather(n, in)

	case "ReduceProd":
		prod := float32(1)
		for _, v := range in[0].Data {
			prod *= v
		}
		return &Tensor{Data: []float32{prod}}, nil

	case "ReduceMean":
		return reduceMeanLast(n, in)

	case "Transpose":
		return transpose(in[0], n.ints("perm"))

	case "Unsqueeze":
		return unsqueeze(n, in)
	case "Squeeze":
		return squeeze(n, in)
	case "Flatten":
		return flatten(n, in)
	case "Concat":
		return concat(n, in)

	case "Conv":
		return convNode(n, in)
	case "Gemm":
		return gemmNode(n, in)
	}
	return nil, fmt.Errorf("operator %q is not implemented", n.Op)
}

func unary(x *Tensor, op func(float32) float32) *Tensor {
	out := NewTensor(x.Shape...)
	for i, v := range x.Data {
		out.Data[i] = op(v)
	}
	return out
}

// axesOf reads an axes list from the attribute (opset ≤ 12) or the second input (opset ≥ 13).
func axesOf(n *Node, in []*Tensor) []int64 {
	if a := n.ints("axes"); a != nil {
		return a
	}
	if len(in) > 1 && in[1] != nil {
		out := make([]int64, len(in[1].Data))
		for i, v := range in[1].Data {
			out[i] = int64(v)
		}
		return out
	}
	return nil
}

// reshaped returns a view of x under a new shape: reshaping never moves data in row-major order.
func reshaped(x *Tensor, shape []int) *Tensor {
	return &Tensor{Shape: shape, Data: x.Data}
}

func unsqueeze(n *Node, in []*Tensor) (*Tensor, error) {
	shape := append([]int(nil), in[0].Shape...)
	for _, a := range axesOf(n, in) {
		axis := int(a)
		if axis < 0 {
			axis += len(shape) + 1
		}
		if axis < 0 || axis > len(shape) {
			return nil, fmt.Errorf("Unsqueeze axis %d is out of range for shape %v", a, in[0].Shape)
		}
		shape = append(shape[:axis], append([]int{1}, shape[axis:]...)...)
	}
	return reshaped(in[0], shape), nil
}

func squeeze(n *Node, in []*Tensor) (*Tensor, error) {
	drop := map[int]bool{}
	for _, a := range axesOf(n, in) {
		axis := int(a)
		if axis < 0 {
			axis += len(in[0].Shape)
		}
		if axis < 0 || axis >= len(in[0].Shape) {
			return nil, fmt.Errorf("Squeeze axis %d is out of range for shape %v", a, in[0].Shape)
		}
		drop[axis] = true
	}
	shape := make([]int, 0, len(in[0].Shape))
	for i, d := range in[0].Shape {
		if !drop[i] {
			shape = append(shape, d)
		}
	}
	return reshaped(in[0], shape), nil
}

func flatten(n *Node, in []*Tensor) (*Tensor, error) {
	axis := 1
	if a, ok := n.Attr["axis"]; ok {
		axis = int(a.Int)
	}
	if axis < 0 {
		axis += len(in[0].Shape)
	}
	if axis < 0 || axis > len(in[0].Shape) {
		return nil, fmt.Errorf("Flatten axis %d is out of range for shape %v", axis, in[0].Shape)
	}
	rows := count(in[0].Shape[:axis])
	return reshaped(in[0], []int{rows, in[0].Size() / rows}), nil
}

func gather(n *Node, in []*Tensor) (*Tensor, error) {
	axis := int(n.Attr["axis"].Int)
	if axis != 0 || len(in[0].Shape) != 1 {
		return nil, fmt.Errorf("only rank-1 Gather along axis 0 is supported, got shape %v axis %d",
			in[0].Shape, axis)
	}
	out := NewTensor(in[1].Shape...)
	for i, raw := range in[1].Data {
		idx := int(raw)
		if idx < 0 {
			idx += len(in[0].Data)
		}
		if idx < 0 || idx >= len(in[0].Data) {
			return nil, fmt.Errorf("Gather index %v is out of range for %d elements", raw, len(in[0].Data))
		}
		out.Data[i] = in[0].Data[idx]
	}
	return out, nil
}

// reduceMeanLast averages over the final axis. Statistics pooling reduces over time, which is the
// last axis in every graph this package targets; any other axis is rejected rather than guessed at.
func reduceMeanLast(n *Node, in []*Tensor) (*Tensor, error) {
	x := in[0]
	axes := n.ints("axes")
	last := len(x.Shape) - 1
	if len(axes) != 1 || (int(axes[0]) != -1 && int(axes[0]) != last) {
		return nil, fmt.Errorf("only ReduceMean over the last axis is supported, got axes %v", axes)
	}
	keepDims := true
	if a, ok := n.Attr["keepdims"]; ok && a.Int == 0 {
		keepDims = false
	}

	width := x.Shape[last]
	if width == 0 {
		return nil, fmt.Errorf("ReduceMean over an empty axis")
	}
	shape := append([]int(nil), x.Shape[:last]...)
	if keepDims {
		shape = append(shape, 1)
	}
	rows := x.Size() / width
	out := &Tensor{Shape: shape, Data: make([]float32, rows)}
	for r := range rows {
		var sum float32
		for _, v := range x.Data[r*width : (r+1)*width] {
			sum += v
		}
		out.Data[r] = sum / float32(width)
	}
	return out, nil
}

func concat(n *Node, in []*Tensor) (*Tensor, error) {
	a, ok := n.Attr["axis"]
	if !ok {
		return nil, fmt.Errorf("Concat without an axis attribute")
	}
	axis := int(a.Int)
	if axis < 0 {
		axis += len(in[0].Shape)
	}
	if axis < 0 || axis >= len(in[0].Shape) {
		return nil, fmt.Errorf("Concat axis %d is out of range for shape %v", axis, in[0].Shape)
	}

	shape := append([]int(nil), in[0].Shape...)
	for _, t := range in[1:] {
		if len(t.Shape) != len(shape) {
			return nil, fmt.Errorf("Concat operands differ in rank: %v and %v", in[0].Shape, t.Shape)
		}
		shape[axis] += t.Shape[axis]
	}
	out := NewTensor(shape...)

	// Everything left of the axis iterates; everything right of it is contiguous, so each operand
	// contributes one uninterrupted block per outer position.
	outer := count(shape[:axis])
	stride := out.Size() / outer
	at := 0
	for _, t := range in {
		block := t.Size() / outer
		for o := range outer {
			copy(out.Data[o*stride+at:], t.Data[o*block:(o+1)*block])
		}
		at += block
	}
	return out, nil
}

func convNode(n *Node, in []*Tensor) (*Tensor, error) {
	if g := n.Attr["group"].Int; g > 1 {
		return nil, fmt.Errorf("grouped convolution (group=%d) is not supported", g)
	}
	for _, d := range n.ints("dilations", 1, 1) {
		if d != 1 {
			return nil, fmt.Errorf("dilated convolution is not supported")
		}
	}
	if a, ok := n.Attr["auto_pad"]; ok && a.Str != "" && a.Str != "NOTSET" {
		return nil, fmt.Errorf("auto_pad %q is not supported; export with explicit pads", a.Str)
	}
	stride := n.ints("strides", 1, 1)
	pads := n.ints("pads", 0, 0, 0, 0)
	if len(stride) != 2 || len(pads) != 4 {
		return nil, fmt.Errorf("only 2-D convolution is supported (strides %v, pads %v)", stride, pads)
	}

	var bias *Tensor
	if len(in) > 2 {
		bias = in[2]
	}
	return conv2d(in[0], in[1], bias,
		[]int{int(stride[0]), int(stride[1])},
		[]int{int(pads[0]), int(pads[1]), int(pads[2]), int(pads[3])})
}

func gemmNode(n *Node, in []*Tensor) (*Tensor, error) {
	if n.Attr["transA"].Int == 1 {
		return nil, fmt.Errorf("Gemm with transA is not supported")
	}
	if a, ok := n.Attr["alpha"]; ok && a.Float != 1 {
		return nil, fmt.Errorf("Gemm with alpha=%v is not supported", a.Float)
	}
	if b, ok := n.Attr["beta"]; ok && b.Float != 1 {
		return nil, fmt.Errorf("Gemm with beta=%v is not supported", b.Float)
	}
	a, b := in[0], in[1]
	if len(a.Shape) != 2 || len(b.Shape) != 2 {
		return nil, fmt.Errorf("Gemm wants two matrices, got %v and %v", a.Shape, b.Shape)
	}

	if n.Attr["transB"].Int == 1 { // b arrives as [N,K]; the kernel wants [K,N]
		var err error
		if b, err = transpose(b, []int64{1, 0}); err != nil {
			return nil, err
		}
	}
	rows, inner := a.Shape[0], a.Shape[1]
	if b.Shape[0] != inner {
		return nil, fmt.Errorf("Gemm inner dimensions disagree: %d and %d", inner, b.Shape[0])
	}
	cols := b.Shape[1]

	out := NewTensor(rows, cols)
	gemm(rows, inner, cols, a.Data, b.Data, out.Data)

	if len(in) > 2 && in[2] != nil {
		if len(in[2].Data) != cols {
			return nil, fmt.Errorf("Gemm bias has %d entries, expected %d", len(in[2].Data), cols)
		}
		for r := range rows {
			row := out.Data[r*cols : (r+1)*cols]
			for i := range row {
				row[i] += in[2].Data[i]
			}
		}
	}
	return out, nil
}
