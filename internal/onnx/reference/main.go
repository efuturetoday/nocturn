// Command reference writes the gomlx reference embedding to JSON, so nocturn can keep the
// cross-implementation check without keeping the dependency.
//
// This is a separate Go module on purpose. gomlx pulls in a large dependency tree, and nocturn's
// whole claim is a single binary with none of it; a nested go.mod keeps it out of the parent
// module's graph entirely. The cost is that the workspace at the repository root does not cover
// this directory, so every go command here needs GOWORK=off:
//
//	GOWORK=off go run -tags noxla . "$NOCTURN_SPEAKER_MODEL" ../testdata/wespeaker_resnet34.golden.json
//
// -tags noxla holds gomlx to its pure-Go backend; without it the build wants XLA shared libraries.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/gomlx/compute"
	_ "github.com/gomlx/gomlx/backends/default"
	"github.com/gomlx/gomlx/core/graph"
	"github.com/gomlx/gomlx/core/tensors"
	"github.com/gomlx/gomlx/ml/model"
	"github.com/gomlx/onnx-gomlx/onnx/parser"
)

const frames = 300

type golden struct {
	Model     string    `json:"model"`
	Reference string    `json:"reference"`
	Input     string    `json:"input"`
	Frames    int       `json:"frames"`
	MelBins   int       `json:"mel_bins"`
	Embedding []float32 `json:"embedding"`
}

func main() {
	path, out := os.Args[1], os.Args[2]

	m, err := parser.ParseFile(path)
	if err != nil {
		panic(err)
	}
	defer m.Close()
	store := model.NewStore()
	if err := m.VariablesToScope(store.RootScope()); err != nil {
		panic(err)
	}
	inNames, _ := m.Inputs()
	outNames, _ := m.Outputs()

	feats := make([][][]float32, 1)
	feats[0] = make([][]float32, frames)
	for t := range frames {
		row := make([]float32, 80)
		for k := range 80 {
			row[k] = float32(math.Sin(float64(t*80+k) * 0.01))
		}
		feats[0][t] = row
	}

	backend := compute.MustNew()
	defer backend.Finalize()
	res := model.MustCallOnceN(backend, store,
		func(scope *model.Scope, inputs []*graph.Node) []*graph.Node {
			return m.CallGraph(scope, inputs[0].Graph(),
				map[string]*graph.Node{inNames[0]: inputs[0]}, outNames...)
		}, feats)

	emb, err := tensors.CopyFlatData[float32](res[0])
	if err != nil {
		panic(err)
	}

	g := golden{
		Model:     "wespeaker_en_voxceleb_resnet34.onnx",
		Reference: "gomlx v0.28.0 + onnx-gomlx v0.5.0, pure-Go backend",
		Input:     "feats[1,300,80], element (t,k) = sin(0.01*(t*80+k))",
		Frames:    frames,
		MelBins:   80,
		Embedding: emb,
	}
	buf, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(out, append(buf, '\n'), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %s — %d dimensions\n", out, len(emb))
}
