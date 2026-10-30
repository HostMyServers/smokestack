package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The probe protocol used to be reachable only through a Unix socket, where
// being able to connect was the whole authentication. Serving the same three
// endpoints on the public listener removes that property, so each one has to
// earn it back from the token — and the target list is the reason to care:
// it carries every active target, private ones included.
func TestProbeProtocolOverHTTPNeedsAToken(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	ProbeRoutes(mux, store, NewTargetCache(store), NewWriter(store, nil))

	p, err := store.CreateProbe("remote-1", "Remote 1", "Lyon, FR")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.IssueProbeToken(p.ID)
	if err != nil {
		t.Fatal(err)
	}

	type req struct{ method, path, body string }
	calls := []req{
		{"GET", "/probe/v1/targets", ""},
		{"POST", "/probe/v1/measurements", `[]`},
		{"POST", "/probe/v1/traceroutes", `[]`},
	}
	do := func(c req, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	refused := map[string]string{
		"no header at all":         "",
		"a bare token":             tok,
		"the wrong scheme":         "Basic " + tok,
		"a token one char off":     "Bearer " + tok + "x",
		"a token that was made up": "Bearer 0123456789abcdef0123456789abcdef",
	}
	for _, c := range calls {
		for why, auth := range refused {
			if got := do(c, auth).Code; got != http.StatusUnauthorized {
				t.Errorf("%s with %s: got %d, want 401", c.path, why, got)
			}
		}
		if got := do(c, "Bearer "+tok).Code; got != http.StatusOK {
			t.Errorf("%s with the issued token: got %d, want 200", c.path, got)
		}
	}

	// The two ways an operator stops a probe have to stop it here too, or
	// disabling one would leave it reading the target list indefinitely.
	if err := store.SetProbeEnabled(p.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := do(calls[0], "Bearer "+tok).Code; got != http.StatusUnauthorized {
		t.Errorf("a disabled probe still reads the target list: got %d, want 401", got)
	}
	if err := store.SetProbeEnabled(p.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeProbeToken(p.ID); err != nil {
		t.Fatal(err)
	}
	if got := do(calls[0], "Bearer "+tok).Code; got != http.StatusUnauthorized {
		t.Errorf("a revoked token still reads the target list: got %d, want 401", got)
	}
}

// The answer lists private targets, so nothing on the path may keep it. Over
// the socket that was academic; over the network it is the difference between
// a probe reading the list and a cache handing it to whoever asks next.
func TestProbeTargetsAreNeverStorable(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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
	r := httptest.NewRequest("GET", "/probe/v1/targets", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("the target list is storable: Cache-Control %q", cc)
	}
	if strings.Contains(cc, "public") {
		t.Errorf("the target list announces itself as public: Cache-Control %q", cc)
	}
}

// A probe says who it is by presenting a token, never by filling in a field.
// Without this, any enrolled probe could file measurements against another
// one and move a graph it has no business touching.
func TestProbeMeasurementsCarryTheCallersIdentity(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	writer := NewWriter(store, nil)
	mux := http.NewServeMux()
	ProbeRoutes(mux, store, NewTargetCache(store), writer)

	p, err := store.CreateProbe("remote-1", "Remote 1", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.IssueProbeToken(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	tid := mkTarget(t, store, "one")
	if err := store.SetProbeTargets(p.ID, []int64{tid}); err != nil {
		t.Fatal(err)
	}

	// The body claims a probe id that is not the caller's.
	const other = 4242
	body := fmt.Sprintf(`[{"target_id":%d,"probe_id":4242,"ts":100,"sent":1,"lost":0,"host":"h"}]`, tid)
	r := httptest.NewRequest("POST", "/probe/v1/measurements", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("submitting one measurement: got %d", w.Code)
	}

	select {
	case q := <-writer.ch:
		if q.m.ProbeID == other {
			t.Fatal("the body's probe_id was taken at face value")
		}
		if q.m.ProbeID != p.ID {
			t.Fatalf("measurement filed under probe %d, want %d", q.m.ProbeID, p.ID)
		}
	default:
		t.Fatal("nothing reached the writer")
	}
}

// A probe token travels on every request and the target list comes back on
// the same connection. Plain http would hand both to anyone on the path, so
// the probe refuses to start rather than logging a warning nobody reads.
func TestRemoteLinkRefusesAnUnsafeService(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef"
	bad := []struct{ base, token, why string }{
		{"http://example.org", tok, "plain http"},
		{"example.org", tok, "no scheme"},
		{"ftp://example.org", tok, "another scheme"},
		{"https://example.org", "", "no token"},
		{"https://example.org", "   ", "a blank token"},
	}
	for _, c := range bad {
		if _, err := newRemoteLinkHTTP(c.base, c.token); err == nil {
			t.Errorf("%s was accepted", c.why)
		}
	}
	l, err := newRemoteLinkHTTP(" https://example.org/ ", " "+tok+" ")
	if err != nil {
		t.Fatalf("a well-formed service was refused: %v", err)
	}
	if l.base != "https://example.org" {
		t.Errorf("base %q, want the trailing slash and spaces gone", l.base)
	}
	if l.token != tok {
		t.Errorf("token %q, want it trimmed", l.token)
	}
}

// mkTarget creates a target to assign. The assignment refuses ids that do not
// exist, so these tests need real ones rather than invented numbers.
func mkTarget(t *testing.T, store *Store, slug string) int64 {
	t.Helper()
	cat, err := store.CreateCategory("c-"+slug, "Catégorie", "Category", true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: slug, Title: slug,
		Host: "198.51.100.1", Proto: "icmp", IntervalS: 60, Packets: 10,
		SpacingMs: 100, TimeoutMs: 1000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Enrolling a probe must not hand it the whole target list. It sees what it
// was assigned and nothing else, and a probe assigned nothing sees nothing —
// the safe way round for something just enrolled.
func TestARemoteProbeSeesOnlyItsAssignedTargets(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mine := mkTarget(t, store, "mine")
	theirs := mkTarget(t, store, "theirs")

	cache := NewTargetCache(store)
	writer := NewWriter(store, nil)
	mux := http.NewServeMux()
	ProbeRoutes(mux, store, cache, writer)

	p, err := store.CreateProbe("remote-1", "Remote 1", "")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.IssueProbeToken(p.ID)
	if err != nil {
		t.Fatal(err)
	}

	list := func() []*Target {
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
		return resp.Targets
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("a probe assigned nothing was given %d targets", len(got))
	}
	if err := store.SetProbeTargets(p.ID, []int64{mine}); err != nil {
		t.Fatal(err)
	}
	got := list()
	if len(got) != 1 || got[0].ID != mine {
		t.Fatalf("assigned one target, was given %d", len(got))
	}
	for _, x := range got {
		if x.ID == theirs {
			t.Fatal("a target it was not assigned leaked into the list")
		}
	}

	// Reading is bounded, so writing has to be too, or the restriction is
	// decorative: a probe could move any graph by naming the target.
	post := func(path string, body string) map[string]int {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: got %d", path, w.Code)
		}
		var out map[string]int
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	out := post("/probe/v1/measurements",
		fmt.Sprintf(`[{"target_id":%d,"ts":1,"sent":1,"lost":0},{"target_id":%d,"ts":1,"sent":1,"lost":0}]`, mine, theirs))
	if out["accepted"] != 1 || out["refused"] != 1 {
		t.Errorf("measurements: accepted=%d refused=%d, want 1 and 1", out["accepted"], out["refused"])
	}
	out = post("/probe/v1/traceroutes",
		fmt.Sprintf(`[{"target_id":%d,"ts":1},{"target_id":%d,"ts":1}]`, mine, theirs))
	if out["accepted"] != 1 || out["refused"] != 1 {
		t.Errorf("traceroutes: accepted=%d refused=%d, want 1 and 1", out["accepted"], out["refused"])
	}
}

// The restriction must not reach the local probe. It measures everything by
// construction, through a socket only a process on the host can open, and
// restricting it would stop every existing instance measuring anything.
func TestTheLocalProbeKeepsEveryTarget(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := mkTarget(t, store, "a")
	mkTarget(t, store, "b")

	id, err := store.ProbeID("local", "Local", "here", true)
	if err != nil {
		t.Fatal(err)
	}
	cache := NewTargetCache(store)
	mux := http.NewServeMux()
	// The socket's resolver, which is what ServeProbeSocket installs.
	probeEndpoints(mux, cache, NewWriter(store, nil), func(*http.Request) (probeCaller, bool) {
		return probeCaller{id: id}, true
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/probe/v1/targets", nil))
	var resp probeTargetsResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Targets) != 2 {
		t.Fatalf("the local probe was given %d targets, want both", len(resp.Targets))
	}

	// And it takes no assignment at all, rather than accepting one that would
	// look meaningful and change nothing.
	if err := store.SetProbeTargets(id, []int64{a}); err == nil {
		t.Error("the local probe accepted an assignment")
	}
}

func TestAssignmentRefusesATargetThatDoesNotExist(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	real := mkTarget(t, store, "real")
	p, err := store.CreateProbe("remote-1", "Remote 1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetProbeTargets(p.ID, []int64{real, 999999}); err == nil {
		t.Fatal("an unknown target id was accepted")
	}
	// Refused as a whole: a partially applied list would be worse than none,
	// because the operator would believe they saved what they typed.
	set, err := store.ProbeTargetIDs(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 0 {
		t.Errorf("a refused assignment left %d rows behind", len(set))
	}
}
