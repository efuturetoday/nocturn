# internal/onnx testdata

## `wespeaker_resnet34.golden.json`

The 256-dimensional embedding an **independent ONNX implementation** produces for a fixed synthetic
input. `TestMatchesIndependentImplementation` recomputes it with this package and requires cosine
similarity 1.0 to within float32 rounding.

The point is independence. Two implementations agreeing means the operator semantics are right —
padding, stride, the transposed `Gemm` weight, the Bessel correction in statistics pooling. A test
that compared this package against itself would pin nothing.

| | |
|---|---|
| Reference | `gomlx v0.28.0` + `onnx-gomlx v0.5.0`, pure-Go backend |
| Model | `wespeaker_en_voxceleb_resnet34.onnx` |
| Input | `feats[1,300,80]`, element `(t,k) = sin(0.01·(t·80+k))` |

The input is not speech and is not meant to be: it pins arithmetic. Whether the pipeline recognises
people is settled in `internal/speaker` against real recordings.

### Regenerating

The generator lives in `../reference/`. It is **its own Go module**, so `gomlx` never enters
nocturn's dependency graph — the parent module ignores any directory with its own `go.mod`, and
`go.work` does not list it.

```bash
cd internal/onnx/reference
go run -tags noxla . "$NOCTURN_SPEAKER_MODEL" ../testdata/wespeaker_resnet34.golden.json
```

`-tags noxla` keeps gomlx on its pure-Go backend; without it the build wants XLA shared libraries.

Regenerate only when the input specification changes. If this package's output stops matching a
golden file that was not regenerated, that is the test doing its job — do not refresh the golden to
make it pass.

## The model itself is not committed

Checkpoints are 25–40 MB. Point `NOCTURN_SPEAKER_MODEL` at one; tests needing it skip when unset.

```bash
curl -L -o wespeaker_en_voxceleb_resnet34.onnx \
  https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/wespeaker_en_voxceleb_resnet34.onnx
```

(The `speaker-recongition-models` tag is misspelled upstream. It is the correct URL.)
