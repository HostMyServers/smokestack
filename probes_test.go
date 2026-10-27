package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The enrolment half and the verification half were written three releases
// apart, so what matters is not that each works but that they agree: a token
// this code issues must be accepted, and — the part worth a test — a token it
// withdraws must stop being accepted immediately.
//
// Everything here drives the real ingest handler rather than calling the
// store directly. The store could be perfectly consistent and the endpoint
// still let a withdrawn token through, and that is the failure that would
// matter.
func TestIngestFollowsTheIssuedToken(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := &API{store: store, token: "secret", probeID: 1}

	p, err := store.CreateProbe("paris-1", "Paris 1", "Paris, FR")
	if err != nil {
		t.Fatal(err)
	}

	// An empty batch on purpose: this is about who may write, not about what
	// gets written, and it keeps the writer out of the test entirely.
	post := func(tok string) int {
		r := httptest.NewRequest("POST", "/api/v1/ingest",
			strings.NewReader(`{"probe":"paris-1","seq":1,"batch":[]}`))
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		api.ingest(w, r)
		return w.Code
	}

	if got := post(""); got != 401 {
		t.Errorf("a probe with no token issued yet: got %d, want 401", got)
	}
	if got := post("not-a-token"); got != 401 {
		t.Errorf("a made-up token before any was issued: got %d, want 401", got)
	}

	tok, err := store.IssueProbeToken(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := post(tok); got != 200 {
		t.Fatalf("the token that was just issued: got %d, want 200", got)
	}
	if got := post(tok + "x"); got != 401 {
		t.Errorf("a token one character off: got %d, want 401", got)
	}

	// Rotation has to invalidate what it replaces, otherwise rotating after a
	// leak would leave the leaked token working and the operator believing
	// the opposite.
	second, err := store.IssueProbeToken(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second == tok {
		t.Fatal("rotation returned the same token twice")
	}
	if got := post(tok); got != 401 {
		t.Errorf("the token rotation replaced: got %d, want 401", got)
	}
	if got := post(second); got != 200 {
		t.Errorf("the token rotation issued: got %d, want 200", got)
	}

	// The assertion this test exists for.
	if err := store.RevokeProbeToken(p.ID); err != nil {
		t.Fatal(err)
	}
	if got := post(second); got != 401 {
		t.Errorf("a revoked token still writes: got %d, want 401", got)
	}
	// Revocation nulls the stored hash, so the empty token is the one value
	// that could conceivably match it. The handler refuses an empty bearer
	// before it ever compares, and this keeps that true if the order of the
	// two checks is ever rearranged.
	if got := post(""); got != 401 {
		t.Errorf("after revocation an empty token is accepted: got %d, want 401", got)
	}

	// Disabling is the other way to stop a probe, and it has to stop it at
	// the door rather than merely hiding it from the interface.
	third, err := store.IssueProbeToken(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := post(third); got != 200 {
		t.Fatalf("a freshly re-issued token: got %d, want 200", got)
	}
	if err := store.SetProbeEnabled(p.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := post(third); got == 200 {
		t.Error("a disabled probe still writes")
	}
}

// The local probe writes through the Unix socket, which only a process on the
// host can reach. Minting an HTTP credential for it would widen that to the
// whole network for no gain, so the store refuses — and this pins the refusal
// rather than leaving it to a comment.
func TestLocalProbeIsRefusedAToken(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id, err := store.ProbeID("local", "Local", "here", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.IssueProbeToken(id); err == nil {
		t.Fatal("the local probe was issued a token")
	}
	// Disabling or revoking it must not silently appear to work either: both
	// are scoped to remote probes, so the local one is reported as unknown
	// rather than quietly left running.
	if err := store.SetProbeEnabled(id, false); err == nil {
		t.Error("the local probe was disabled through the remote-probe path")
	}
	if err := store.RevokeProbeToken(id); err == nil {
		t.Error("the local probe had a token revoked")
	}
}

func TestProbeSlugRules(t *testing.T) {
	ok := []string{"paris-1", "p1", "a-b-c", "lon", strings.Repeat("a", probeSlugMax)}
	bad := []string{"", "a", "-paris", "paris-", "Paris", "pa ris", "pa_ris", "pa.ris",
		"paris/../etc", strings.Repeat("a", probeSlugMax+1)}
	for _, s := range ok {
		if err := validProbeSlug(s); err != nil {
			t.Errorf("%q should be accepted: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := validProbeSlug(s); err == nil {
			t.Errorf("%q should be refused", s)
		}
	}
}

// Nothing that leaves the server may carry the hash, under any field name. A
// JSON rename is exactly the kind of change that would start shipping it
// without anyone noticing, so this reads the encoded form rather than the
// struct.
func TestProbeListNeverShipsTheHash(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := &API{store: store}

	p, err := store.CreateProbe("lon-1", "London 1", "London, GB")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.IssueProbeToken(p.ID)
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	api.probesGet(w, httptest.NewRequest("GET", "/api/v1/admin/probes", nil), &User{Role: "admin"})
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("listing probes: got %d", w.Code)
	}
	if strings.Contains(body, tok) {
		t.Error("the listing carries the token itself")
	}
	if strings.Contains(body, hashToken(tok)) {
		t.Error("the listing carries the token hash")
	}
	if !strings.Contains(body, `"has_token":true`) {
		t.Error("the listing does not say the probe has a token")
	}
}

// Where a probe measures from is observed by default and stated when the
// observation would be wrong. The precedence is the whole feature, so it is
// pinned in both directions: stating wins, clearing hands it back.
func TestProbeAddressPrefersWhatTheOperatorStated(t *testing.T) {
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

	only := func() Probe {
		list, err := store.Probes()
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range list {
			if x.ID == p.ID {
				return x
			}
		}
		t.Fatal("the probe disappeared from the listing")
		return Probe{}
	}

	// Nothing observed and nothing stated: no answer rather than a wrong one.
	if got := only().From; got != "" {
		t.Errorf("a probe that never connected reports %q, want empty", got)
	}

	// Connecting is what teaches the service the address.
	r := httptest.NewRequest("GET", "/probe/v1/targets", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.RemoteAddr = "198.51.100.9:40000"
	mux.ServeHTTP(httptest.NewRecorder(), r)

	if got := only().SeenAddress; got != "198.51.100.9" {
		t.Errorf("observed address %q, want 198.51.100.9", got)
	}
	if got := only().From; got != "198.51.100.9" {
		t.Errorf("with nothing stated, from is %q, want the observed address", got)
	}

	// Stating one overrides it, which is the point: behind NAT, what arrives
	// is not where the probe measures from.
	if err := store.SetProbeAddress(p.ID, "203.0.113.4"); err != nil {
		t.Fatal(err)
	}
	if got := only().From; got != "203.0.113.4" {
		t.Errorf("the stated address did not win: from is %q", got)
	}
	if got := only().SeenAddress; got != "198.51.100.9" {
		t.Errorf("stating an address overwrote the observed one: %q", got)
	}

	// A later connection still updates what is observed, so clearing the
	// override later lands on something current rather than something stale.
	r2 := httptest.NewRequest("GET", "/probe/v1/targets", nil)
	r2.Header.Set("Authorization", "Bearer "+tok)
	r2.RemoteAddr = "198.51.100.77:40001"
	mux.ServeHTTP(httptest.NewRecorder(), r2)
	if got := only().From; got != "203.0.113.4" {
		t.Errorf("a new connection overrode the stated address: from is %q", got)
	}

	// Clearing hands the question back to observation.
	if err := store.SetProbeAddress(p.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := only().From; got != "198.51.100.77" {
		t.Errorf("after clearing, from is %q, want the most recent observed address", got)
	}
}

func TestProbeAddressMustBeAnAddress(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := store.CreateProbe("remote-1", "Remote 1", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"not-an-ip", "example.org", "203.0.113.999", "203.0.113.4/32", "203.0.113.4:80"} {
		if err := store.SetProbeAddress(p.ID, bad); err == nil {
			t.Errorf("%q was accepted as an address", bad)
		}
	}
	for _, ok := range []string{"203.0.113.4", "2001:db8::1", "  203.0.113.5  "} {
		if err := store.SetProbeAddress(p.ID, ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}
