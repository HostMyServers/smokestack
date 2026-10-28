package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A traceroute is asked for because something looked wrong from somewhere.
// Running it from a different vantage point answers a question nobody asked,
// so requests go to the probe that decides the target.
func TestRequestsGoToTheProbeThatDecidesTheTarget(t *testing.T) {
	var q traceQueue
	const mine, theirs, orphan = int64(1), int64(2), int64(3)
	q.Push(mine)
	q.Push(theirs)
	q.Push(orphan)

	now := time.Now().Unix()
	got := q.PopFor(map[int64]bool{mine: true}, now)
	if len(got) != 1 || got[0] != mine {
		t.Fatalf("took %v, want only the target it decides", got)
	}

	// What it did not take is still there for the probe it belongs to, rather
	// than dropped on the floor.
	got = q.PopFor(map[int64]bool{theirs: true}, now)
	if len(got) != 1 || got[0] != theirs {
		t.Fatalf("the other probe took %v, want its own target", got)
	}

	// And a request whose probe never comes back is not stuck forever: past
	// the grace period any probe may run it, which is what the queue did for
	// everything before routing existed.
	got = q.PopFor(map[int64]bool{}, now+traceClaimGrace)
	if len(got) != 1 || got[0] != orphan {
		t.Fatalf("after the grace period took %v, want the abandoned request", got)
	}
	if left := q.PopFor(nil, now+traceClaimGrace*10); len(left) != 0 {
		t.Errorf("%v left in the queue", left)
	}
}

// The grace period must not be a way of taking work early.
func TestAProbeDoesNotTakeAnotherProbesFreshRequest(t *testing.T) {
	var q traceQueue
	q.Push(7)
	now := time.Now().Unix()
	if got := q.PopFor(map[int64]bool{}, now+traceClaimGrace-1); len(got) != 0 {
		t.Fatalf("took %v one second before the grace period", got)
	}
	if got := q.PopFor(map[int64]bool{}, now+traceClaimGrace); len(got) != 1 {
		t.Fatal("did not take the request once the grace period had passed")
	}
}

// An instance that never enrols a remote probe must behave exactly as it did
// before targets had a primary at all: its local probe decides everything.
func TestTheLocalProbeDecidesEverythingByDefault(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := mkTarget(t, store, "a")
	b := mkTarget(t, store, "b")

	local, err := store.ProbeID("local", "Local", "here", true)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.PrimaryTargets(local)
	if err != nil {
		t.Fatal(err)
	}
	if !set[a] || !set[b] {
		t.Fatalf("the local probe decides %v, want both targets", set)
	}

	// Handing one target to a remote probe takes it away from the local one,
	// which is the whole point: two probes must not both run the same work.
	p, err := store.CreateProbe("remote-1", "Remote 1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetProbeTargets(p.ID, []int64{a}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetPrimaryProbe(a, p.ID); err != nil {
		t.Fatal(err)
	}
	set, _ = store.PrimaryTargets(local)
	if set[a] {
		t.Error("the local probe still decides a target handed to a remote probe")
	}
	if !set[b] {
		t.Error("the local probe stopped deciding a target nobody took")
	}
	set, _ = store.PrimaryTargets(p.ID)
	if !set[a] || set[b] {
		t.Errorf("the remote probe decides %v, want only the target it was given", set)
	}

	// Handing it back is spelled zero, and restores exactly the state above.
	if err := store.SetTargetPrimaryProbe(a, 0); err != nil {
		t.Fatal(err)
	}
	set, _ = store.PrimaryTargets(local)
	if !set[a] || !set[b] {
		t.Error("clearing the primary did not hand the target back to the local probe")
	}
}

// Pointing a target at a probe that may not measure it would produce a target
// whose status nobody can report, and nothing on the page that set it would
// say why.
func TestAPrimaryMustBeAProbeThatMeasuresTheTarget(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tid := mkTarget(t, store, "t")
	other := mkTarget(t, store, "other")

	local, err := store.ProbeID("local", "Local", "here", true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreateProbe("remote-1", "Remote 1", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SetTargetPrimaryProbe(tid, p.ID); err == nil {
		t.Error("a probe that is not assigned the target was accepted as its primary")
	}
	if err := store.SetTargetPrimaryProbe(tid, 999999); err == nil {
		t.Error("an unknown probe was accepted")
	}
	if err := store.SetTargetPrimaryProbe(tid, local); err == nil {
		t.Error("the local probe was accepted by id rather than by leaving it unset")
	}

	if err := store.SetProbeTargets(p.ID, []int64{tid}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetPrimaryProbe(tid, p.ID); err != nil {
		t.Fatalf("an assigned probe was refused: %v", err)
	}
	if err := store.SetTargetPrimaryProbe(other, p.ID); err == nil {
		t.Error("the probe became primary for a target it was not assigned")
	}

	// A disabled probe cannot be chosen: it would be a target whose status is
	// decided by something that is not running.
	if err := store.SetProbeTargets(p.ID, []int64{tid, other}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProbeEnabled(p.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetPrimaryProbe(other, p.ID); err == nil {
		t.Error("a disabled probe was accepted as a primary")
	}
}

// End to end over the registered routes, because the store being consistent
// says nothing about what the endpoint actually hands a probe.
func TestTheEndpointRoutesRequestsByPrimary(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := mkTarget(t, store, "a")
	b := mkTarget(t, store, "b")

	mux := http.NewServeMux()
	ProbeRoutes(mux, store, NewTargetCache(store), NewWriter(store, nil))

	p, err := store.CreateProbe("remote-1", "Remote 1", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.IssueProbeToken(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetProbeTargets(p.ID, []int64{a, b}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetPrimaryProbe(a, p.ID); err != nil {
		t.Fatal(err)
	}

	// b stays with the local probe, so the remote one must be handed a only.
	traceRequests.Push(a)
	traceRequests.Push(b)
	defer traceRequests.PopFor(nil, 0)

	r := httptest.NewRequest("GET", "/probe/v1/targets", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("fetching targets: got %d", w.Code)
	}
	var resp probeTargetsResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.TraceRequests) != 1 || resp.TraceRequests[0] != a {
		t.Fatalf("the remote probe was handed %v, want only the target it decides", resp.TraceRequests)
	}
}
