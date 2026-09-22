package cost

import (
	"math"
	"testing"
)

var (
	haiku  = Rates{Read: 0.1, Write: 2.0, Out: 5}
	sonnet = Rates{Read: 0.2, Write: 4.0, Out: 10}
	opus   = Rates{Read: 0.5, Write: 10.0, Out: 25}
	fable  = Rates{Read: 0.25, Write: 20.0, Out: 50}
)

// The table from the design doc. Read down a column: which pair of tiers is
// involved matters more than how big the conversation is.
func TestBreakEvenTableFromTheSpec(t *testing.T) {
	cases := []struct {
		name   string
		from   Rates
		to     Rates
		cached int
		want   float64
	}{
		{"opus->haiku 80k", opus, haiku, 80_000, 2.1},
		{"opus->haiku 100k", opus, haiku, 100_000, 2.4},
		{"opus->haiku 500k", opus, haiku, 500_000, 4.0},
		{"opus->sonnet 80k", opus, sonnet, 80_000, 5.6},
		{"opus->sonnet 100k", opus, sonnet, 100_000, 6.3},
		{"opus->sonnet 500k", opus, sonnet, 500_000, 10.6},
		{"sonnet->haiku 80k", sonnet, haiku, 80_000, 8.4},
		{"sonnet->haiku 100k", sonnet, haiku, 100_000, 9.5},
		{"sonnet->haiku 500k", sonnet, haiku, 500_000, 15.8},
	}
	for _, c := range cases {
		got, ok := BreakEvenTurns(c.from, c.to, c.cached, 2000)
		if !ok {
			t.Errorf("%s: switching never pays off, want %.1f turns", c.name, c.want)
			continue
		}
		if math.Abs(got-c.want) > 0.05 {
			t.Errorf("%s: break-even = %.2f turns, want %.1f", c.name, got, c.want)
		}
	}
}

func TestHorizonFiveAllowsOpusToHaikuAndHoldsTheRest(t *testing.T) {
	cases := []struct {
		name string
		from Rates
		to   Rates
		want bool
	}{
		{"opus->haiku", opus, haiku, true},
		{"opus->sonnet", opus, sonnet, false},
		{"sonnet->haiku", sonnet, haiku, false},
	}
	for _, c := range cases {
		if got := SwitchPaysOff(c.from, c.to, 100_000, 2000, 5); got != c.want {
			t.Errorf("%s at 100k horizon 5 = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAnUpgradeNeverPaysOff(t *testing.T) {
	if _, ok := BreakEvenTurns(haiku, opus, 100_000, 2000); ok {
		t.Error("moving to a more expensive tier must report that it never pays off")
	}
	if SwitchPaysOff(haiku, opus, 100_000, 2000, 1000) {
		t.Error("no horizon makes an upgrade pay for itself on cost alone")
	}
}

func TestFableReadsAtTheCheaperRate(t *testing.T) {
	// Fable reads at 0.025x, so leaving it saves far less than its headline
	// price suggests. Compare against a hypothetical fable priced at 0.1x.
	expensiveReads := Rates{Read: 1.0, Write: 20.0, Out: 50}
	cheap, _ := BreakEvenTurns(fable, haiku, 100_000, 2000)
	dear, _ := BreakEvenTurns(expensiveReads, haiku, 100_000, 2000)
	if !(cheap > dear) {
		t.Errorf("cheap fable reads must make leaving it slower to pay off: %.2f vs %.2f", cheap, dear)
	}
}

func TestZeroContextAlwaysSwitches(t *testing.T) {
	if !SwitchPaysOff(opus, haiku, 0, 2000, 1) {
		t.Error("with nothing cached there is no rebuild to pay for")
	}
}

func TestHorizonZeroNeverSwitches(t *testing.T) {
	if SwitchPaysOff(opus, haiku, 100_000, 2000, 0) {
		t.Error("a zero-turn horizon cannot repay any rebuild")
	}
}
