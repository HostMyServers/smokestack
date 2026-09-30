package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func maintStore(t *testing.T) (*Store, int64) {
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

// What a window is allowed to be. A window that stops nothing, ends
// before it starts, or lasts a season is a mistake, not a maintenance.
func TestMaintenanceValidation(t *testing.T) {
	store, id := maintStore(t)
	now := time.Now().Unix()
	bad := []struct {
		name string
		m    Maintenance
	}{
		{"no title", Maintenance{TargetID: id, StartsAt: now, EndsAt: now + 3600, StopAlerts: true}},
		{"ends first", Maintenance{TargetID: id, Title: "x", StartsAt: now + 3600, EndsAt: now, StopAlerts: true}},
		{"empty window", Maintenance{TargetID: id, Title: "x", StartsAt: now, EndsAt: now, StopAlerts: true}},
		{"too long", Maintenance{TargetID: id, Title: "x", StartsAt: now, EndsAt: now + 60*86400, StopAlerts: true}},
		{"stops nothing", Maintenance{TargetID: id, Title: "x", StartsAt: now, EndsAt: now + 3600}},
		{"unknown target", Maintenance{TargetID: 9999, Title: "x", StartsAt: now, EndsAt: now + 3600, StopAlerts: true}},
	}
	for _, c := range bad {
		if _, err := store.CreateMaintenance(&c.m); err == nil {
			t.Errorf("%s should have been refused", c.name)
		}
	}
	ok := Maintenance{TargetID: id, Title: "Router upgrade", Note: "Provider window",
		StartsAt: now, EndsAt: now + 7200, StopAlerts: true, StopProbe: true}
	if _, err := store.CreateMaintenance(&ok); err != nil {
		t.Fatalf("a well-formed window should be accepted: %v", err)
	}
}

// Which window is in force, and what happens when two overlap: the
// strictest wins, otherwise a permissive window would cancel one that
// deliberately stops the measurement.
func TestMaintenanceActive(t *testing.T) {
	store, id := maintStore(t)
	now := time.Now().Unix()
	mk := func(start, end int64, alerts, probe bool) {
		m := Maintenance{TargetID: id, Title: "w", StartsAt: start, EndsAt: end,
			StopAlerts: alerts, StopProbe: probe}
		if _, err := store.CreateMaintenance(&m); err != nil {
			t.Fatal(err)
		}
	}
	mk(now-3600, now+3600, true, false) // alerting only
	mk(now-600, now+600, false, true)   // and a stricter one inside it
	mk(now+86400, now+90000, true, true)

	act := store.ActiveMaintenances(now)
	m, ok := act[id]
	if !ok {
		t.Fatal("a window covering now should be active")
	}
	if !m.StopAlerts || !m.StopProbe {
		t.Errorf("overlapping windows keep the strictest: %+v", m)
	}
	if m.EndsAt != now+3600 {
		t.Errorf("the active window ends at the latest of the two, got %d", m.EndsAt-now)
	}
	// Before and after, nothing is in force.
	if len(store.ActiveMaintenances(now-7200)) != 0 {
		t.Error("a future window must not be active")
	}
	if len(store.ActiveMaintenances(now+7200)) != 0 {
		t.Error("a finished window must not be active")
	}
}

// A window that stops the measurement takes the target out of what the
// probe is given, in both probe modes, since both read the same cache.
func TestMaintenanceStopsProbing(t *testing.T) {
	store, id := maintStore(t)
	now := time.Now().Unix()
	cache := NewTargetCache(store)
	if len(cache.Targets()) != 1 {
		t.Fatalf("the target should be measured before any window: %d", len(cache.Targets()))
	}
	// Alerting only: the measurement carries on, which is the point of
	// having two switches rather than one.
	m := Maintenance{TargetID: id, Title: "quiet", StartsAt: now - 60, EndsAt: now + 3600,
		StopAlerts: true}
	mid, err := store.CreateMaintenance(&m)
	if err != nil {
		t.Fatal(err)
	}
	cache.refresh()
	if len(cache.Targets()) != 1 {
		t.Error("a window that only silences alerts must not stop the measurement")
	}
	// Now stop the measurement too.
	m.ID, m.StopProbe = mid, true
	if err := store.UpdateMaintenance(&m); err != nil {
		t.Fatal(err)
	}
	cache.refresh()
	if len(cache.Targets()) != 0 {
		t.Error("a window that stops the measurement must take the target out of the probe")
	}
	// And it comes back by itself when the window closes.
	m.StartsAt, m.EndsAt = now-7200, now-3600
	if err := store.UpdateMaintenance(&m); err != nil {
		t.Fatal(err)
	}
	cache.refresh()
	if len(cache.Targets()) != 1 {
		t.Error("the target must be measured again once the window is over")
	}
}

// The incident is still recorded — it is what explains the gap — but
// nobody is woken up for a restart that was announced.
func TestMaintenanceSilencesAlerts(t *testing.T) {
	store, id := maintStore(t)
	clock := time.Now().Unix()
	cfg := defaultAlertConfig()
	cfg.Enabled, cfg.AfterMinutes, cfg.RepeatHours = true, 1, 6
	cfg.Recipients = "noc@example.net"
	if err := store.SetAlertConfig(cfg); err != nil {
		t.Fatal(err)
	}
	sent := 0
	a := NewAlerter(store)
	a.now = func() int64 { return clock }
	a.send = func(_ AlertConfig, _, _ string) error { sent++; return nil }
	a.trace = func(int64) {}

	// An incident opens, and is sustained past the threshold.
	a.Tick(map[int64]string{id: "crit"}, nil)
	clock += 600
	if a.Tick(map[int64]string{id: "crit"}, nil); sent == 0 {
		t.Fatal("a sustained incident should alert without a maintenance window")
	}
	// A second target, in maintenance, must stay quiet in the same state.
	cat, _ := store.CreateCategory("c2", "C2", "C2", true)
	quiet, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "q", Title: "Q",
		Host: "192.0.2.2", Proto: "icmp", IntervalS: 60, Packets: 5, SpacingMs: 100,
		TimeoutMs: 1000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMaintenance(&Maintenance{TargetID: quiet, Title: "upgrade",
		StartsAt: clock - 60, EndsAt: clock + 7200, StopAlerts: true}); err != nil {
		t.Fatal(err)
	}
	before := sent
	a.Tick(map[int64]string{quiet: "crit"}, nil)
	clock += 600
	a.Tick(map[int64]string{quiet: "crit"}, nil)
	if sent != before {
		t.Errorf("a target in maintenance must not alert, %d message(s) sent", sent-before)
	}
	// The incident itself is recorded, so the gap can be explained later.
	inc, err := a.Incidents(20)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, i := range inc {
		if i.TargetID == quiet {
			found = true
			if i.NotifiedAt != 0 {
				t.Error("the incident must be recorded as not notified")
			}
		}
	}
	if !found {
		t.Error("the incident must still be recorded during a maintenance window")
	}
}

// Availability ignores the passes measured inside a window that stops the
// measurement: an announced restart must not degrade the figure the way
// an outage does.
func TestMaintenanceExcludedFromAvailability(t *testing.T) {
	store, id := maintStore(t)
	now := time.Now()
	base := now.Unix() - 3600
	rec := func(i int, lost int) {
		m := Measurement{TargetID: id, ProbeID: 1, TS: base + int64(i)*60, Sent: 5, Lost: lost}
		for j := 0; j < 5-lost; j++ {
			m.RTTus = append(m.RTTus, 1000)
		}
		if err := store.Record(m); err != nil {
			t.Fatal(err)
		}
	}
	// Forty clean passes, then ten dead ones during a planned window.
	for i := 0; i < 40; i++ {
		rec(i, 0)
	}
	for i := 40; i < 50; i++ {
		rec(i, 5)
	}
	from, to := base-60, now.Unix()+60

	// Without a window, the ten dead passes count: 80 %.
	w := store.availWindow(id, 1, "1h", from, to)
	if w.Pct == nil || *w.Pct < 79.9 || *w.Pct > 80.1 {
		t.Fatalf("without a window the figure should be 80 %%, got %v", w.Pct)
	}
	// Declaring the window, measurement stopped, removes them entirely.
	if _, err := store.CreateMaintenance(&Maintenance{TargetID: id, Title: "reboot",
		StartsAt: base + 40*60, EndsAt: base + 51*60, StopAlerts: true, StopProbe: true}); err != nil {
		t.Fatal(err)
	}
	w = store.availWindow(id, 1, "1h", from, to)
	if w.Pct == nil || *w.Pct < 99.9 {
		t.Fatalf("the planned window should be excluded, got %v", w.Pct)
	}
	if w.Excluded != 10 {
		t.Errorf("ten passes should be reported as excluded, got %d", w.Excluded)
	}
	// A window that only silences alerts changes nothing: the measurement
	// happened, and it is real.
	store2, id2 := maintStore(t)
	for i := 0; i < 10; i++ {
		m := Measurement{TargetID: id2, ProbeID: 1, TS: base + int64(i)*60, Sent: 5, Lost: 5}
		if err := store2.Record(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store2.CreateMaintenance(&Maintenance{TargetID: id2, Title: "quiet",
		StartsAt: base, EndsAt: to, StopAlerts: true}); err != nil {
		t.Fatal(err)
	}
	w2 := store2.availWindow(id2, 1, "1h", from, to)
	if w2.Pct == nil || *w2.Pct != 0 {
		t.Errorf("silencing alerts must not rewrite the measurements: %v", w2.Pct)
	}
}

// A window covering the whole period leaves the figure unknown rather
// than at zero or at a hundred: nothing was measured to judge.
func TestMaintenanceCoveringEverything(t *testing.T) {
	store, id := maintStore(t)
	now := time.Now()
	base := now.Unix() - 1800
	for i := 0; i < 20; i++ {
		m := Measurement{TargetID: id, ProbeID: 1, TS: base + int64(i)*60, Sent: 5, Lost: 5}
		if err := store.Record(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateMaintenance(&Maintenance{TargetID: id, Title: "all of it",
		StartsAt: base - 600, EndsAt: now.Unix() + 600, StopAlerts: true, StopProbe: true}); err != nil {
		t.Fatal(err)
	}
	w := store.availWindow(id, 1, "1h", base-60, now.Unix()+60)
	if w.Pct != nil {
		t.Errorf("a period entirely under maintenance has no availability, got %v", *w.Pct)
	}
}

// Overlapping windows must not be subtracted twice, which would push the
// excluded count above the number of passes measured.
func TestMergeRanges(t *testing.T) {
	in := [][2]int64{{100, 200}, {150, 250}, {400, 500}, {250, 300}}
	got := mergeRanges(in)
	want := [][2]int64{{100, 300}, {400, 500}}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

// The public endpoint shows a private target's window to nobody, and
// never leaks who created it.
func TestMaintenancePublicScope(t *testing.T) {
	store, id := maintStore(t)
	now := time.Now().Unix()
	tg, _ := store.TargetByID(id)
	tg.Public = false
	if err := store.UpdateTarget(tg); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMaintenance(&Maintenance{TargetID: id, Title: "secret window",
		StartsAt: now - 60, EndsAt: now + 3600, StopAlerts: true,
		CreatedBy: "ops@example.net"}); err != nil {
		t.Fatal(err)
	}
	api := &API{store: store, token: "secret", probeID: 1}
	// The banner cache is a package global: another test having warmed it
	// must not decide what this one sees.
	availCache.items = map[string]availEntry{}
	call := func(auth bool) (int, string) {
		req := httptest.NewRequest("GET", "/api/v1/maintenance", nil)
		if auth {
			req.Header.Set("Authorization", "Bearer secret")
		}
		rec := httptest.NewRecorder()
		api.maintenancePublic(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := call(false)
	if code != 200 {
		t.Fatalf("HTTP %d", code)
	}
	if strings.Contains(body, "secret window") {
		t.Error("a private target's maintenance window must not be public")
	}
	if _, body := call(true); !strings.Contains(body, "secret window") {
		t.Error("an authenticated caller should see it")
	} else if strings.Contains(body, "ops@example.net") {
		t.Error("who declared the window is not published")
	}
}
