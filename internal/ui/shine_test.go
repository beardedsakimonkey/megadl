package ui

import (
	"math"
	"testing"
	"time"

	"megadl/internal/engine"
)

// The ramp is what keeps the sweep from banding, so it is checked as a shape:
// full at a band's center, out by its half width, and falling the whole way
// between with no flat step to read as an edge.
func TestShineIntensityFallsSmoothlyFromBandCenter(t *testing.T) {
	if got := shineIntensity(0); got != 1 {
		t.Fatalf("shineIntensity(0) = %v, want 1", got)
	}
	if got := shineIntensity(shineHalfWidth); got != 0 {
		t.Fatalf("shineIntensity(%v) = %v, want 0 at the band's edge", shineHalfWidth, got)
	}
	prev := 1.0
	for step := 1; step <= 20; step++ {
		x := shineHalfWidth * float64(step) / 20
		got := shineIntensity(x)
		if got >= prev {
			t.Fatalf("shineIntensity(%v) = %v, want less than %v", x, got, prev)
		}
		prev = got
	}
	// Half a cell of travel has to move the color, or the sweep would advance
	// in whole-cell jumps however finely it is paced.
	if shineIntensity(2) == shineIntensity(2.5) {
		t.Fatal("shineIntensity is flat across half a cell, want a sub-cell ramp")
	}
}

// The bands repeat, so a cell is lit the same by whichever one is nearest.
func TestShineIntensityRepeatsEveryBand(t *testing.T) {
	for _, x := range []float64{0, 1.7, 4.5, 7, 13.2} {
		a, b := shineIntensity(x), shineIntensity(x+shineSpacing)
		if math.Abs(a-b) > 1e-9 {
			t.Fatalf("shineIntensity(%v) = %v, but %v one band along", x, a, b)
		}
		if c := shineIntensity(x - shineSpacing); math.Abs(a-c) > 1e-9 {
			t.Fatalf("shineIntensity(%v) = %v, but %v one band back", x, a, c)
		}
	}
	// Between bands the bar is its own flat green, so they read as separate
	// passes rather than one rippling strip.
	if got := shineIntensity(shineSpacing / 2); got != 0 {
		t.Fatalf("shineIntensity(%v) = %v, want unlit between bands", shineSpacing/2, got)
	}
}

// The bands travel leftward, one cell of the bar for every cell the pattern
// advances.
func TestShineSweepTravelsLeft(t *testing.T) {
	// Searched over the right-hand stretch of a statusbar-width bar, which is
	// narrow enough to hold one band and so has one answer.
	brightest := func(offset float64) int {
		best, at := -1.0, 0
		for cell := 6; cell < 20; cell++ {
			if v := shineIntensity(float64(cell) + offset); v > best {
				best, at = v, cell
			}
		}
		return at
	}
	prev := brightest(0)
	for step := 1.0; step <= 3; step++ {
		at := brightest(step)
		if at != prev-1 {
			t.Fatalf("band moved from cell %d to %d after one cell of travel, want %d",
				prev, at, prev-1)
		}
		prev = at
	}
}

// A running clock advances with real time and wraps with the period, so the
// travel is the elapsed time's rather than the repaint loop's.
func TestShineClockAdvancesWithTheTimeItRuns(t *testing.T) {
	base := time.Unix(0, 0)
	c := shineClock{}.start(base)
	if got := c.offsetAt(base); got != 0 {
		t.Fatalf("offsetAt(start) = %v, want 0", got)
	}
	if got := c.offsetAt(base.Add(shinePeriod / 2)); math.Abs(got-shineSpacing/2) > 1e-9 {
		t.Fatalf("offsetAt(half a period) = %v, want %v", got, shineSpacing/2)
	}
	if got := c.offsetAt(base.Add(shinePeriod)); math.Abs(got) > 1e-9 {
		t.Fatalf("offsetAt(a full period) = %v, want it wrapped to 0", got)
	}
	// A clock already running keeps its anchor, so the repeated starts the
	// repaint loop makes cannot rewind the sweep.
	if got := c.start(base.Add(shinePeriod / 2)); got != c {
		t.Fatalf("start() on a running clock = %+v, want it unchanged", got)
	}
}

// A stopped clock is what a held queue draws from, and the phase has to carry
// across the stop: pausing leaves the bands where they were and resuming picks
// them up there, so neither reads as a jump.
func TestShineClockCarriesItsPhaseAcrossAStop(t *testing.T) {
	base := time.Unix(0, 0)
	run := shineClock{}.start(base)

	paused := base.Add(shinePeriod / 3)
	held := run.stop(paused)
	if math.Abs(held.phase-run.offsetAt(paused)) > 1e-9 {
		t.Fatalf("stopped at %v, want the offset it was travelling at %v",
			held.phase, run.offsetAt(paused))
	}
	// Held, the bands stand still however long the pause lasts.
	for _, d := range []time.Duration{0, shinePeriod / 2, 37 * shinePeriod} {
		if got := held.offsetAt(paused.Add(d)); got != held.phase {
			t.Fatalf("held offset after %v = %v, want %v", d, got, held.phase)
		}
	}
	// Resumed, they set off from there rather than from wherever the wall
	// clock has got to in the meantime.
	resumed := paused.Add(10 * shinePeriod)
	again := held.start(resumed)
	if got := again.offsetAt(resumed); got != held.phase {
		t.Fatalf("offset on resuming = %v, want the held %v", got, held.phase)
	}
	if got := again.offsetAt(resumed.Add(shinePeriod / 4)); math.Abs(got-math.Mod(held.phase+shineSpacing/4, shineSpacing)) > 1e-9 {
		t.Fatalf("offset a quarter period after resuming = %v, want it travelling on from %v",
			got, held.phase)
	}
}

// The sweep reports movement and nothing else: the bands travel only while a
// file is actually being fetched. Everywhere else they stand still, so a bar
// never blinks between lit and unlit as the queue picks its way from one file
// to the next.
func TestSweepRunsOnlyWhileFetching(t *testing.T) {
	running := engine.Snapshot{ActiveID: 3, CurrentFile: "episode-01.mkv"}
	if !sweepRuns(running) {
		t.Fatal("sweepRuns(running) is false, want the bands travelling")
	}
	for name, snap := range map[string]engine.Snapshot{
		"idle":           {},
		"between files":  {ActiveID: 3},
		"held queue":     {ActiveID: 3, CurrentFile: "episode-01.mkv", Paused: true},
		"held with none": {Paused: true},
	} {
		if sweepRuns(snap) {
			t.Fatalf("sweepRuns(%s) is true, want the bands standing still", name)
		}
	}
}

// The sweep colors each cell's halves separately, which without color to give
// them leaves a row of half blocks rather than a bar. Where the profile has
// none, the plain bar has to be what gets drawn — including here, which is why
// the rest of the package's bar tests can go on comparing exact strings.
func TestShineProgressBarFallsBackWithoutColor(t *testing.T) {
	if colorEnabled() {
		t.Skip("profile emits color, so the sweep draws its own bar")
	}
	for _, frac := range []float64{0, 0.01, 0.25, 0.4, 0.625, 0.99, 1} {
		plain := progressBar(20, frac)
		if got := shineProgressBar(20, frac, 3.5); got != plain {
			t.Fatalf("sweeping bar at %v = %q, want the plain bar %q", frac, got, plain)
		}
	}
	// A bar too narrow to draw is still too narrow to draw.
	if got := shineProgressBar(1, 0.5, 0); got != "" {
		t.Fatalf("shineProgressBar(1, ...) = %q, want empty", got)
	}
}
