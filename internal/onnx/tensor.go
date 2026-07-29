package onnx

import "fmt"

// Tensor is a dense row-major float32 tensor. Weights carry their graph name; activations produced
// during a run leave it empty. One type serves both so the executor never has to convert between a
// "weight" and a "value" — an initializer is simply a value that was already there.
type Tensor struct {
	Name  string
	Shape []int
	Data  []float32
}

// NewTensor allocates a zeroed tensor of the given shape.
func NewTensor(shape ...int) *Tensor {
	return &Tensor{Shape: shape, Data: make([]float32, count(shape))}
}

// Size is the number of elements.
func (t *Tensor) Size() int { return len(t.Data) }

func (t *Tensor) String() string { return fmt.Sprintf("Tensor%v", t.Shape) }

func count(shape []int) int {
	n := 1
	for _, d := range shape {
		n *= d
	}
	return n
}

// strides returns the row-major strides of a shape.
func strides(shape []int) []int {
	s := make([]int, len(shape))
	acc := 1
	for i := len(shape) - 1; i >= 0; i-- {
		s[i] = acc
		acc *= shape[i]
	}
	return s
}

// broadcastShape computes the result shape of combining a and b under numpy broadcasting rules.
func broadcastShape(a, b []int) ([]int, error) {
	n := max(len(a), len(b))
	out := make([]int, n)
	for i := range n {
		da, db := 1, 1
		if j := i - (n - len(a)); j >= 0 {
			da = a[j]
		}
		if j := i - (n - len(b)); j >= 0 {
			db = b[j]
		}
		switch {
		case da == db, db == 1:
			out[i] = da
		case da == 1:
			out[i] = db
		default:
			return nil, fmt.Errorf("shapes %v and %v do not broadcast", a, b)
		}
	}
	return out, nil
}

// elementwise applies op with numpy broadcasting. The statistics-pooling head needs it to
// subtract a per-channel mean from a full time series, which is the only non-trivial case here.
func elementwise(a, b *Tensor, op func(x, y float32) float32) (*Tensor, error) {
	shape, err := broadcastShape(a.Shape, b.Shape)
	if err != nil {
		return nil, err
	}
	out := NewTensor(shape...)

	// Per-operand strides over the result shape, zeroed on the axes being broadcast so that
	// stepping the result index leaves those operands in place.
	project := func(t *Tensor) []int {
		src, full := strides(t.Shape), make([]int, len(shape))
		off := len(shape) - len(t.Shape)
		for i, d := range t.Shape {
			if d != 1 {
				full[off+i] = src[i]
			}
		}
		return full
	}
	as, bs := project(a), project(b)

	idx := make([]int, len(shape))
	for i := range out.Data {
		ai, bi := 0, 0
		for d := range shape {
			ai += idx[d] * as[d]
			bi += idx[d] * bs[d]
		}
		out.Data[i] = op(a.Data[ai], b.Data[bi])

		for d := len(shape) - 1; d >= 0; d-- {
			if idx[d]++; idx[d] < shape[d] {
				break
			}
			idx[d] = 0
		}
	}
	return out, nil
}

// transpose permutes axes; perm[i] names the source axis that becomes result axis i.
func transpose(x *Tensor, perm []int64) (*Tensor, error) {
	if len(perm) != len(x.Shape) {
		return nil, fmt.Errorf("perm %v does not match shape %v", perm, x.Shape)
	}
	shape := make([]int, len(perm))
	for i, p := range perm {
		if p < 0 || int(p) >= len(x.Shape) {
			return nil, fmt.Errorf("perm %v is out of range for shape %v", perm, x.Shape)
		}
		shape[i] = x.Shape[p]
	}
	out := NewTensor(shape...)
	src := strides(x.Shape)

	idx := make([]int, len(shape))
	for i := range out.Data {
		off := 0
		for d, p := range perm {
			off += idx[d] * src[p]
		}
		out.Data[i] = x.Data[off]

		for d := len(shape) - 1; d >= 0; d-- {
			if idx[d]++; idx[d] < shape[d] {
				break
			}
			idx[d] = 0
		}
	}
	return out, nil
}
