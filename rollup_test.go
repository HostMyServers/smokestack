package main

import (
	"fmt"
	"testing"
)

// rollupPasses counts the passes each level of the cascade holds.
func rollupPasses(t *testing.T, store *Store) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, g := range cascade {
		var n int64
		if err := store.mx.QueryRow(fmt.Sprintf(
			`SELECT COALESCE(SUM(passes),0) FROM %s`, g.table)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[g.table] = n
	}
	return out
}

func rollupPass(ts int64) queuedMeasure {
	return queuedMeasure{m: Measurement{TargetID: 1, ProbeID: 1, TS: ts, Sent: 4,
		RTTus: []float64{1000, 1100, 1200, 1300}}}
}

// The cascade runs every 30 s on windows ending at the current second, so
// every bucket of every level ends up as the oldest one of a window. Up to
// 0.6.11 it came out of there with part of its passes, then none: graphs
// beyond four days held the last six hours.
func TestRollupKeepsEveryPassAsTheWindowSlides(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Ten passes early in their minute, where the trailing edge of the
	// window used to cut them off.
	start := int64(1_700_000_000)/86400*86400 + 3600
	var batch []queuedMeasure
	for i := int64(0); i < 10; i++ {
		batch = append(batch, rollupPass(start+i*60+5))
	}
	if err := store.RecordBatch(batch); err != nil {
		t.Fatal(err)
	}
	// The windows reach back 2 h, 6 h and 3 days: sweep each trailing edge
	// across the ten minutes, at the pace of the background loop.
	for _, back := range []int64{2 * 3600, 6 * 3600, 3 * 86400} {
		for now := start + back - 60; now <= start+back+15*60; now += 30 {
			if err := store.RollupTick(now); err != nil {
				t.Fatal(err)
			}
		}
	}
	for table, n := range rollupPasses(t, store) {
		if n != 10 {
			t.Errorf("%s holds %d passes out of 10", table, n)
		}
	}
}

// What the old cascade erased comes back from the raw passes, which it
// never touched.
func TestRebuildRollupsRestoresTheErasedAggregates(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := int64(1_700_000_000)
	var batch []queuedMeasure
	for ts := now - 30*3600; ts < now; ts += 60 {
		batch = append(batch, rollupPass(ts))
	}
	if err := store.RecordBatch(batch); err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildRollups(now); err != nil {
		t.Fatal(err)
	}
	for table, n := range rollupPasses(t, store) {
		if n != 1800 {
			t.Errorf("%s holds %d passes out of 1800", table, n)
		}
	}
}

// The purge cuts the raw passes in the middle of their oldest minute, which
// roll_1m still holds whole: rebuilding it from what is left would lose
// half of it.
func TestRebuildRollupsLeavesAPurgedMinuteWhole(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := int64(1_700_000_000)
	cut := now - 48*3600
	oldest := cut - mod(cut, 60)
	if cut-oldest != 20 {
		t.Fatalf("the cut falls %d s into its minute, the test needs it inside", cut-oldest)
	}
	batch := []queuedMeasure{rollupPass(oldest + 10), rollupPass(oldest + 40)}
	if err := store.RecordBatch(batch); err != nil {
		t.Fatal(err)
	}
	if err := store.RollupRange("samples", "roll_1m", 60, oldest, oldest+60); err != nil {
		t.Fatal(err)
	}
	if err := store.Purge(now); err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildRollups(now); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := store.mx.QueryRow(`SELECT passes FROM roll_1m WHERE bucket=?`, oldest).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("the oldest minute holds %d passes after the rebuild, want 2", n)
	}
}
