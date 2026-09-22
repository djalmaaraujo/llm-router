package config

import "testing"

func TestRatesForAppliesTheCacheMultipliers(t *testing.T) {
	opus := RatesFor("opus")
	if opus.Read != 0.5 || opus.Write != 10 || opus.Out != 25 {
		t.Errorf("opus rates = %+v, want read 0.5 write 10 out 25", opus)
	}
	fable := RatesFor("fable")
	if fable.Read != 0.25 {
		t.Errorf("fable read = %v, want 0.25 from the 0.025x multiplier", fable.Read)
	}
	if got := RatesFor("nope"); got.Read != 0 || got.Write != 0 || got.Out != 0 {
		t.Errorf("an unknown tier must return the zero value, got %+v", got)
	}
}
