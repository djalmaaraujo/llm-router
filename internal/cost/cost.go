// Package cost answers one question: does changing models save more than
// rebuilding the prompt cache costs? Anthropic keys the cache on the model, so
// any switch re-pays cache creation for the whole conversation prefix.
//
// It imports nothing on purpose. Every input is a number the caller measured.
package cost

// Rates are US dollars per million tokens for one tier, with the cache
// multipliers already applied.
type Rates struct {
	Read  float64 // cached input tokens
	Write float64 // input tokens written to the cache
	Out   float64 // output tokens
}

// BreakEvenTurns reports how many turns a switch needs before it has paid for
// the cache rebuild, and whether it ever does.
//
//	stay(h)   = h * (cached*from.Read + out*from.Out)
//	switch(h) = cached*to.Write + out*to.Out + (h-1)*(cached*to.Read + out*to.Out)
//
// ok is false when running on `to` costs at least as much per turn as staying,
// which is every upgrade: no horizon repays it, and the decision belongs to
// capability rather than to cost.
func BreakEvenTurns(from, to Rates, cachedTokens, outputTokens int) (float64, bool) {
	cached := float64(cachedTokens) / 1e6
	out := float64(outputTokens) / 1e6

	stay := cached*from.Read + out*from.Out
	first := cached*to.Write + out*to.Out
	later := cached*to.Read + out*to.Out

	if stay <= later {
		return 0, false
	}
	return (first - later) / (stay - later), true
}

// SwitchPaysOff reports whether the switch repays itself within horizon turns.
func SwitchPaysOff(from, to Rates, cachedTokens, outputTokens, horizon int) bool {
	turns, ok := BreakEvenTurns(from, to, cachedTokens, outputTokens)
	return ok && turns <= float64(horizon)
}
