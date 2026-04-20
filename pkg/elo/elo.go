package elo

import "math"

const kFactor = 32

func Calculate(winner, loser int) (newWinner, newLoser int) {
	expWinner := expected(winner, loser)
	expLoser := expected(loser, winner)
	newWinner = winner + int(float64(kFactor)*(1-expWinner))
	newLoser = loser + int(float64(kFactor)*(0-expLoser))
	return
}

func WithinRange(a, b, rangeVal int) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= rangeVal
}

func expected(a, b int) float64 {
	return 1.0 / (1.0 + math.Pow(10, float64(b-a)/400.0))
}
