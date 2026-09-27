package runlog

import "math"

// shannonEntropy is gitleaks' shannonEntropy (detect/utils.go) at the pinned
// commit: mean bits per character over the runes, with a byte-length
// denominator. The port keeps that denominator rather than correcting it, so a
// threshold carried from upstream means the same here as there.
func shannonEntropy(data string) (entropy float64) {
	if data == "" {
		return 0
	}

	charCounts := make(map[rune]int)
	for _, char := range data {
		charCounts[char]++
	}

	invLength := 1.0 / float64(len(data))
	for _, count := range charCounts {
		freq := float64(count) * invLength
		entropy -= freq * math.Log2(freq)
	}

	return entropy
}
