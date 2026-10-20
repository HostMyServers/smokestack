package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// minePriv reaches the signing key of a federation, so that a request can be
// forged the way an older or hand-rolled client would send one.
func minePriv(t *testing.T, f *Federation) ed25519.PrivateKey {
	t.Helper()
	if f.priv == nil {
		t.Fatal("no key")
	}
	return f.priv
}

// An instance installed without --public-url has federation.base_url empty
// in its configuration, so the profile it announces carries no URL. Nothing
// checked that before sending, so the operator's own missing setting came
// back as HTTP 400 "incomplete request" from the far end.
func TestPairingNeedsOurOwnAddress(t *testing.T) {
	// The far end: a real instance, which will answer the profile request and
	// then judge the pairing request.
	remote := newFed(t, "AS64501", "Remote", "https://remote.example.net")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/fed/profile"):
			writeJSON(w, remote.Profile())
		case strings.HasSuffix(r.URL.Path, "/fed/pairing/request"):
			if _, err := remote.HandlePairingRequest(r); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			writeJSON(w, map[string]string{"state": "pending"})
		default:
			writeErr(w, 404, "no")
		}
	}))
	defer srv.Close()
	fedAllowPrivateForTests = true
	t.Cleanup(func() { fedAllowPrivateForTests = false })

	// Ours: AS set, as the operator would have, but no base_url.
	mine := newFed(t, "AS64500", "Mine", "")
	if u := mine.Profile().URL; u != "" {
		t.Fatalf("the fixture is wrong, URL = %q", u)
	}
	_, err := mine.RequestPairing(PairingInput{
		URL:           srv.URL,
		Justification: "We peer at a common exchange and would like a second view.",
		ContactName:   "NOC",
	})
	if err == nil {
		t.Fatal("pairing succeeded with no base_url")
	}
	t.Logf("operator sees: %v", err)
	if strings.Contains(err.Error(), "incomplete request") {
		t.Errorf("the message blames the far end for a setting missing here: %v", err)
	}
	for _, want := range []string{"base_url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should name %q so the operator can fix it: %v", want, err)
		}
	}

	// And if a request from an older instance arrives with the field empty
	// anyway, the far end names it rather than answering "incomplete
	// request" and leaving the other operator to guess.
	short := pairingPayload{Token: "t", ASN: "AS64500", PubKey: "k"} // no URL
	_, err = remote.HandlePairingRequest(signedFactory(
		minePriv(t, mine), "AS64500", "AS64501", "POST",
		"/api/v1/fed/pairing/request", short)())
	if err == nil {
		t.Fatal("a payload with no URL was accepted")
	}
	if !strings.Contains(err.Error(), "url is missing") {
		t.Errorf("the refusal should name the missing field: %v", err)
	}
	t.Logf("far end says: %v", err)
}

// federation.base_url in a file was the only way to set the one thing a
// pairing request cannot do without, so an operator who installed without
// --public-url could not pair and had nowhere in the interface to fix it.
// The back-office fills the gap, and the file still wins where both are set
// — the same order as the AS number and the organisation.
func TestInstanceAddressComesFromTheBackOfficeToo(t *testing.T) {
	f := newFed(t, "AS64500", "Mine", "") // no base_url, as a plain install has
	if u := f.Profile().URL; u != "" {
		t.Fatalf("URL = %q with no base_url and nothing saved", u)
	}

	site := f.store.Site()
	site.FedURL = "https://latency.example.net/"
	if err := f.store.SetSite(site); err != nil {
		t.Fatal(err)
	}
	// Announced without its trailing slash, like a configured one: the
	// paths are appended to it.
	if u := f.Profile().URL; u != "https://latency.example.net" {
		t.Errorf("URL = %q, want the address saved in the back-office", u)
	}

	// And the file wins when it is set, so an operator who pins it there
	// cannot have it changed from the interface.
	g := newFed(t, "AS64500", "Mine", "https://from-the-file.example.net")
	gs := g.store.Site()
	gs.FedURL = "https://from-the-back-office.example.net"
	if err := g.store.SetSite(gs); err != nil {
		t.Fatal(err)
	}
	if u := g.Profile().URL; u != "https://from-the-file.example.net" {
		t.Errorf("URL = %q, want the configured one to win", u)
	}
}

// An address a peer cannot reach is worse than none: it is announced in a
// pairing request and fetched back by whoever is asked.
func TestInstanceAddressIsCheckedWhenSaved(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := &API{store: store, token: "secret", probeID: 1}

	save := func(v string) int {
		body, _ := json.Marshal(Site{Title: "T", FedURL: v})
		req := httptest.NewRequest("PUT", "/api/v1/site", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		api.sitePut(w, req)
		return w.Code
	}
	for _, c := range []struct {
		v    string
		want int
	}{
		{"https://latency.example.net", 200},
		{"", 200}, // empty is allowed: the file may carry it
		{"not a url", 400},
		{"ftp://latency.example.net", 400},
		{"javascript:alert(1)", 400},
	} {
		if got := save(c.v); got != c.want {
			t.Errorf("saving %q returned %d, want %d", c.v, got, c.want)
		}
	}
}
