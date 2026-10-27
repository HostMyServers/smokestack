package main

import (
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

	// The body claims a probe id that is not the caller's.
	const other = 4242
	body := `[{"target_id":1,"probe_id":4242,"ts":100,"sent":1,"lost":0,"host":"h"}]`
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
