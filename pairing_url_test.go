package main

import (
	"crypto/ed25519"
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
