package fuzz

// minimize deterministically shrinks input while pred keeps returning
// true. Used to produce minimal crash reproducers.
func minimize(input []byte, pred func([]byte) bool) []byte {
	out := make([]byte, len(input))
	copy(out, input)
	// Try deleting chunks of decreasing size.
	for chunk := len(out) / 2; chunk >= 1; chunk /= 2 {
		for i := 0; i+chunk <= len(out); {
			cand := make([]byte, 0, len(out)-chunk)
			cand = append(cand, out[:i]...)
			cand = append(cand, out[i+chunk:]...)
			if len(cand) > 0 && pred(cand) {
				out = cand
			} else {
				i += chunk
			}
		}
	}
	// Try zeroing individual bytes.
	for i := 0; i < len(out); i++ {
		if out[i] == 0 {
			continue
		}
		cand := make([]byte, len(out))
		copy(cand, out)
		cand[i] = 0
		if pred(cand) {
			out = cand
		}
	}
	return out
}
