package knowledge

import "math"

// normalize scales a vector to unit length, so a cosine similarity later is a plain dot product.
//
// Done once, when the vector is stored, rather than on every comparison: a search touches every
// vector in the index and would otherwise recompute the same two magnitudes for each of them. The
// same trick internal/speaker uses for voices, for the same reason.
//
// A zero vector has no direction to preserve and is returned unchanged — dividing would produce
// NaNs, which spread silently through every score they touch.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// dot is the similarity between two unit vectors: their cosine, without the division.
//
// Mismatched lengths are impossible by the time this runs — the index refuses to load against a
// different embedder — so this treats it as a programming error and returns 0 rather than growing a
// second error path through the ranking code.
func dot(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
