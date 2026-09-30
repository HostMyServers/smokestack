package main

import (
	"testing"
	"time"
)

func availStore(t *testing.T) (*Store, int64) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	cat, _ := store.CreateCategory("c", "C", "C", true)
	id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "t", Title: "T",
		Host: "192.0.2.1", Proto: "icmp", IntervalS: 60, Packets: 5, SpacingMs: 100,
		TimeoutMs: 1000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return store, id
}

// A pass counts as down only when nothing came back. Partial loss proves
// the target answers, which is the whole point of the definition.
func TestAvailabilityCountsPasses(t *testing.T) {
	store, id := availStore(t)
	now := time.Now()
	base := now.Unix() - 3000
	rec := func(i int, sent, lost int) {
		m := Measurement{TargetID: id, ProbeID: 1, TS: base + int64(i)*60,
			Sent: sent, Lost: lost}
		for j := 0; j < sent-lost; j++ {
			m.RTTus = append(m.RTTus, 1000)
		}
		if err := store.Record(m); err != nil {
			t.Fatal(err)
		}
	}
	// Ten passes: eight clean, one losing four packets out of five, one
	// entirely silent. Availability is 90 %, not the 92 % a packet count
	// would give, and the two numbers must not be confused.
	for i := 0; i < 8; i++ {
		rec(i, 5, 0)
	}
	rec(8, 5, 4)
	rec(9, 5, 5)

	w := store.availWindow(id, 1, "1h", base-60, now.Unix()+60)
	if w.Passes != 10 || w.Down != 1 {
		t.Fatalf("ten passes, one silent, got passes=%d down=%d", w.Passes, w.Down)
	}
	if w.Pct == nil || *w.Pct < 89.9 || *w.Pct > 90.1 {
		t.Fatalf("availability should be 90 %%, got %v", w.Pct)
	}
}

// Nothing measured is not the same as nothing available: the percentage
// stays absent rather than reading zero.
func TestAvailabilityWithoutData(t *testing.T) {
	store, id := availStore(t)
	w := store.availWindow(id, 1, "24h", time.Now().Unix()-86400, time.Now().Unix())
	if w.Pct != nil {
		t.Errorf("an empty window has no percentage, got %v", *w.Pct)
	}
	if w.Passes != 0 || w.Partial {
		t.Errorf("an empty window is empty, not partial: %+v", w)
	}
}

// A window whose measurements start well after its beginning is partial,
// so a target created last week does not advertise a yearly figure.
func TestAvailabilityPartialWindow(t *testing.T) {
	store, id := availStore(t)
	now := time.Now()
	// One full hour of passes, and nothing before it.
	for i := 0; i < 60; i++ {
		if err := store.Record(Measurement{TargetID: id, ProbeID: 1,
			TS: now.Unix() - int64(i)*60, Sent: 5, Lost: 0,
			RTTus: []float64{1000, 1000, 1000, 1000, 1000}}); err != nil {
			t.Fatal(err)
		}
	}
	full := store.availWindow(id, 1, "1h", now.Unix()-3600, now.Unix()+60)
	if full.Partial {
		t.Errorf("an hour covered from its start is not partial: %+v", full)
	}
	for i := 0; i+1 < len(cascade); i++ {
		if err := store.RollupRange(cascade[i].table, cascade[i+1].table,
			cascade[i+1].secs, now.Unix()-7200, now.Unix()+60); err != nil {
			t.Fatal(err)
		}
	}
	// Two days asked for, one hour measured: the figure exists but must
	// say it does not cover the window.
	old := store.availWindow(id, 1, "2d", now.Unix()-2*86400, now.Unix()+60)
	if old.Pct == nil {
		t.Fatalf("the hour measured should still produce a figure: %+v", old)
	}
	if !old.Partial {
		t.Errorf("two days covered only by the last hour is partial: %+v", old)
	}
	// And a window with no measurement at all is unknown rather than
	// partial: there is nothing to qualify.
	empty := store.availWindow(id, 1, "1y", now.Unix()-365*86400, now.Unix()-300*86400)
	if empty.Pct != nil || empty.Partial {
		t.Errorf("an unmeasured window is unknown, not partial: %+v", empty)
	}
}

// The counters must survive the aggregation cascade, otherwise every
// window longer than two days would read as unknown.
func TestAvailabilitySurvivesRollup(t *testing.T) {
	store, id := availStore(t)
	now := time.Now().Unix()
	from := now - 7200
	for i := 0; i < 60; i++ {
		lost := 0
		if i%10 == 0 {
			lost = 5 // six silent passes out of sixty
		}
		m := Measurement{TargetID: id, ProbeID: 1, TS: from + int64(i)*60,
			Sent: 5, Lost: lost}
		for j := 0; j < 5-lost; j++ {
			m.RTTus = append(m.RTTus, 1000)
		}
		if err := store.Record(m); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i+1 < len(cascade); i++ {
		if err := store.RollupRange(cascade[i].table, cascade[i+1].table,
			cascade[i+1].secs, from-86400, now+86400); err != nil {
			t.Fatal(err)
		}
	}
	for _, tbl := range []string{"roll_1m", "roll_5m", "roll_1h", "roll_1d"} {
		var passes, down int64
		if err := store.mx.QueryRow("SELECT COALESCE(SUM(passes),0),COALESCE(SUM(down),0) FROM "+
			tbl+" WHERE target_id=?", id).Scan(&passes, &down); err != nil {
			t.Fatal(err)
		}
		if passes != 60 || down != 6 {
			t.Errorf("%s: passes=%d down=%d, expected 60 and 6", tbl, passes, down)
		}
	}
}

// Which table answers which window. Reading a yearly figure out of
// samples would return almost nothing, since samples keep two days.
func TestAvailTable(t *testing.T) {
	cases := []struct {
		span int64
		want string
	}{
		{3600, "samples"},
		{86400, "roll_1m"},
		{7 * 86400, "roll_1h"},
		{30 * 86400, "roll_1h"},
		{365 * 86400, "roll_1d"},
	}
	for _, c := range cases {
		if got := availTable(c.span); got != c.want {
			t.Errorf("a %d s window should read %s, got %s", c.span, c.want, got)
		}
	}
	// Every table the picker names must be one the cascade keeps long
	// enough, otherwise the figure silently covers less than it claims.
	keep := map[string]int64{}
	for _, g := range cascade {
		keep[g.table] = g.keep
	}
	for _, w := range availWindows {
		tbl := availTable(w.secs)
		if k, ok := keep[tbl]; !ok {
			t.Errorf("window %s reads unknown table %s", w.label, tbl)
		} else if k != 0 && k < w.secs {
			t.Errorf("window %s reads %s, kept only %d s", w.label, tbl, k)
		}
	}
}
