// Package onnx runs a small ONNX graph in pure Go — no CGO, no shared library, no WebAssembly, and
// no dependency outside the standard library. It exists because nocturn is a single binary with no
// foreign runtime, and the alternatives all break that: onnxruntime needs CGO plus a .dylib beside
// the binary, ONNX Runtime's WASM build is Emscripten and cannot load into wazero, and the pure-Go
// tensor frameworks that do exist run a speaker-embedding ResNet roughly seventy times slower than
// the kernels here (measured, not assumed — see the benchmarks).
//
// The scope is deliberately narrow: the operator set is what a convolutional speaker-embedding
// network uses, batch size is one, and convolutions are single-group and undilated. This is not a
// general ONNX runtime and must not grow into one. A model that needs an operator this package does
// not have gets a clear error, never a silently wrong answer.
//
// Field numbers below come from onnx.proto; the wire format is walked directly rather than pulling
// in a protobuf runtime for four message types.
//
//	ModelProto.graph      = 7
//	GraphProto.node       = 1 · name = 2 · initializer = 5 · input = 11 · output = 12
//	NodeProto.input       = 1 · output = 2 · name = 3 · op_type = 4 · attribute = 5
//	AttributeProto.name   = 1 · f = 2 · i = 3 · s = 4 · t = 5 · ints = 8
//	TensorProto.dims      = 1 · data_type = 2 · float_data = 4 · name = 8 · raw_data = 9
package onnx

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// maxModelBytes caps what ReadFile will load. A speaker-embedding checkpoint is tens of megabytes;
// anything past this is not a model we intend to run, and refusing early beats discovering it after
// allocating a gigabyte of tensors.
const maxModelBytes = 512 << 20

// dtype values from TensorProto.DataType. Only the two that appear in these checkpoints are read.
const (
	dtypeFloat = 1
	dtypeInt64 = 7
)

// Attr is one operator attribute. Only the kinds this operator set consults are decoded.
type Attr struct {
	Name   string
	Int    int64
	Float  float32
	Str    string
	Ints   []int64
	Tensor *Tensor // Constant nodes carry their value here
}

// Node is one operator in the graph.
type Node struct {
	Op   string
	Name string
	In   []string
	Out  []string
	Attr map[string]Attr
}

// ints returns the named integer-list attribute, or fallback when it is absent.
func (n *Node) ints(name string, fallback ...int64) []int64 {
	if a, ok := n.Attr[name]; ok && a.Ints != nil {
		return a.Ints
	}
	return fallback
}

// Graph is a parsed model, ready to Run.
type Graph struct {
	Name    string
	Nodes   []Node
	Init    map[string]*Tensor // the weights, by tensor name
	Inputs  []string           // graph inputs that are not initializers
	Outputs []string
}

// ReadFile parses an ONNX model from disk.
func ReadFile(path string) (*Graph, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxModelBytes {
		return nil, fmt.Errorf("onnx: %s is %d bytes, over the %d byte limit", path, info.Size(), maxModelBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Read(raw)
}

// Read parses an ONNX model from memory.
func Read(raw []byte) (*Graph, error) {
	body := field(raw, 7)
	if body == nil {
		return nil, fmt.Errorf("onnx: no graph found (not an ONNX model?)")
	}
	g := &Graph{Name: string(field(body, 2)), Init: map[string]*Tensor{}}

	walk(body, 1, func(b []byte) bool {
		g.Nodes = append(g.Nodes, readNode(b))
		return true
	})
	var initErr error
	walk(body, 5, func(b []byte) bool {
		t, err := readTensor(b)
		if err != nil {
			initErr = err
			return false
		}
		g.Init[t.Name] = t
		return true
	})
	if initErr != nil {
		return nil, initErr
	}
	walk(body, 11, func(b []byte) bool {
		if n := string(field(b, 1)); g.Init[n] == nil {
			g.Inputs = append(g.Inputs, n)
		}
		return true
	})
	walk(body, 12, func(b []byte) bool {
		g.Outputs = append(g.Outputs, string(field(b, 1)))
		return true
	})
	if len(g.Nodes) == 0 {
		return nil, fmt.Errorf("onnx: graph %q has no nodes", g.Name)
	}
	return g, nil
}

func readNode(b []byte) Node {
	n := Node{Op: string(field(b, 4)), Name: string(field(b, 3)), Attr: map[string]Attr{}}
	walk(b, 1, func(v []byte) bool { n.In = append(n.In, string(v)); return true })
	walk(b, 2, func(v []byte) bool { n.Out = append(n.Out, string(v)); return true })
	walk(b, 5, func(v []byte) bool {
		a := Attr{Name: string(field(v, 1)), Str: string(field(v, 4))}
		if f := field(v, 2); len(f) == 4 {
			a.Float = math.Float32frombits(binary.LittleEndian.Uint32(f))
		}
		if i := field(v, 3); i != nil {
			a.Int = int64(uvarint(i))
		}
		if t := field(v, 5); t != nil {
			if parsed, err := readTensor(t); err == nil {
				a.Tensor = parsed
			}
		}
		// Repeated ints arrive packed into one length-delimited field or as separate varint
		// fields, depending on which exporter wrote the file. Accept both.
		walk(v, 8, func(p []byte) bool {
			a.Ints = append(a.Ints, varints(p)...)
			return true
		})
		n.Attr[a.Name] = a
		return true
	})
	return n
}

func readTensor(b []byte) (*Tensor, error) {
	t := &Tensor{Name: string(field(b, 8))}
	walk(b, 1, func(v []byte) bool {
		for _, d := range varints(v) {
			t.Shape = append(t.Shape, int(d))
		}
		return true
	})

	dtype := int32(uvarint(field(b, 2)))
	if raw := field(b, 9); len(raw) > 0 {
		switch dtype {
		case dtypeFloat:
			t.Data = make([]float32, len(raw)/4)
			for i := range t.Data {
				t.Data[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
			}
		case dtypeInt64:
			// Shape arithmetic travels as int64. Every such tensor here holds dimensions or
			// axis indices, which are far inside float32's exact-integer range.
			t.Data = make([]float32, len(raw)/8)
			for i := range t.Data {
				t.Data[i] = float32(int64(binary.LittleEndian.Uint64(raw[i*8:])))
			}
		default:
			return nil, fmt.Errorf("onnx: tensor %q has unsupported dtype %d", t.Name, dtype)
		}
		return t, nil
	}

	walk(b, 4, func(v []byte) bool { // float_data, packed
		for len(v) >= 4 {
			t.Data = append(t.Data, math.Float32frombits(binary.LittleEndian.Uint32(v)))
			v = v[4:]
		}
		return true
	})
	return t, nil
}

// --- protobuf wire format ---------------------------------------------------------------------

// Wire types, from the protobuf encoding spec.
const (
	wireVarint = 0
	wire64     = 1
	wireBytes  = 2
	wire32     = 5
)

// walk calls yield with the payload of every field carrying the wanted number, in encounter order,
// and stops early when yield returns false. Malformed input simply ends the walk: a truncated model
// yields fewer fields, and the caller notices through a missing node, weight or output.
func walk(buf []byte, want int32, yield func([]byte) bool) {
	for len(buf) > 0 {
		tag, n := binary.Uvarint(buf)
		if n <= 0 {
			return
		}
		buf = buf[n:]
		num, wire := int32(tag>>3), tag&7

		var val []byte
		switch wire {
		case wireVarint:
			_, n := binary.Uvarint(buf)
			if n <= 0 {
				return
			}
			val, buf = buf[:n], buf[n:]
		case wire64:
			if len(buf) < 8 {
				return
			}
			val, buf = buf[:8], buf[8:]
		case wire32:
			if len(buf) < 4 {
				return
			}
			val, buf = buf[:4], buf[4:]
		case wireBytes:
			size, n := binary.Uvarint(buf)
			if n <= 0 {
				return
			}
			buf = buf[n:]
			if uint64(len(buf)) < size {
				return
			}
			val, buf = buf[:size], buf[size:]
		default:
			return
		}
		if num == want && !yield(val) {
			return
		}
	}
}

// field returns the payload of the first field with the wanted number, or nil.
func field(buf []byte, want int32) []byte {
	var out []byte
	walk(buf, want, func(v []byte) bool { out = v; return false })
	return out
}

// varints decodes a packed repeated varint field.
func varints(b []byte) []int64 {
	var out []int64
	for len(b) > 0 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			return out
		}
		out, b = append(out, int64(v)), b[n:]
	}
	return out
}

func uvarint(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	v, _ := binary.Uvarint(b)
	return v
}
