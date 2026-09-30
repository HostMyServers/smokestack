package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func bdStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func bdTarget(t *testing.T, store *Store, slug, host, proto string, port int) int64 {
	t.Helper()
	catID, err := store.CreateCategory("bd-"+slug, "BD", "BD", true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateTarget(&Target{CategoryID: catID, Slug: slug, Title: slug,
		Host: host, Proto: proto, Port: port, IntervalS: 60, Packets: 5, SpacingMs: 100,
		TimeoutMs: 2000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A literal address has nothing to resolve, and the connect time must be
// the only thing measured: reporting a made-up DNS figure would be worse
// than reporting none.
func TestBreakdownLiteralAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)

	b := measureBreakdown("127.0.0.1", port, 4, 2*time.Second)
	if b.Err != "" {
		t.Fatalf("a listening port should be reachable: %s", b.Err)
	}
	if b.DNSus != 0 {
		t.Errorf("a literal address resolves nothing, got %.0f us of DNS", b.DNSus)
	}
	if b.ConnectUs <= 0 {
		t.Error("the connection should have been timed")
	}
	if b.TLSus != 0 {
		t.Errorf("no handshake is attempted on a plain port, got %.0f us", b.TLSus)
	}
	if b.IP != "127.0.0.1" {
		t.Errorf("the address actually reached should be reported, got %q", b.IP)
	}
}

// A port where nothing listens must be reported as such, with the connect
// attempt still timed: how long the refusal took is itself information.
func TestBreakdownNoListener(t *testing.T) {
	b := measureBreakdown("127.0.0.1", 1, 4, time.Second)
	if b.Err == "" {
		t.Error("a closed port must be reported")
	}
	if !strings.Contains(b.Err, "TCP connection") {
		t.Errorf("the reason should name the step that failed: %q", b.Err)
	}
	if b.TLSus != 0 {
		t.Error("no handshake can follow a failed connection")
	}
}

// A name that does not resolve fails at the first step, and the message
// must say so rather than blaming the connection.
func TestBreakdownBadName(t *testing.T) {
	b := measureBreakdown("no-such-host.invalid", 443, 4, time.Second)
	if b.Err == "" {
		t.Fatal("an unresolvable name must be reported")
	}
	if !strings.Contains(b.Err, "does not resolve") {
		t.Errorf("the reason should name the resolution: %q", b.Err)
	}
	if b.ConnectUs != 0 || b.TLSus != 0 {
		t.Error("nothing can be connected to a name that does not resolve")
	}
}

// On a TLS port, the handshake is timed as its own step, so a slow
// certificate chain is not mistaken for a slow network.
func TestBreakdownTLSStep(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1),
		Subject:     pkix.Name{CommonName: "localhost"},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if tc, ok := c.(*tls.Conn); ok {
				tc.Handshake()
			}
			c.Close()
		}
	}()
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	// The listener is on a random port, so drive the TLS step directly
	// rather than depending on the port number.
	tlsPorts[port] = true
	defer delete(tlsPorts, port)

	b := measureBreakdown("127.0.0.1", port, 4, 3*time.Second)
	if b.Err != "" {
		t.Fatalf("the handshake should have completed: %s", b.Err)
	}
	if b.TLSus <= 0 {
		t.Error("the handshake should have been timed as its own step")
	}
	if b.ConnectUs <= 0 {
		t.Error("the connection should still be timed separately")
	}
	if b.Total() < b.TLSus {
		t.Error("the total cannot be smaller than one of its parts")
	}
}

// Only the last result is kept: this is a diagnostic, not a series, and
// one row per target is the whole storage.
func TestBreakdownKeepsOnlyTheLast(t *testing.T) {
	store := bdStore(t)
	id := bdTarget(t, store, "svc", "192.0.2.1", "tcp", 443)
	store.saveBreakdown(Breakdown{TargetID: id, TS: 100, ConnectUs: 1000})
	store.saveBreakdown(Breakdown{TargetID: id, TS: 200, ConnectUs: 2000})
	var n int
	if err := store.cfg.QueryRow("SELECT COUNT(*) FROM tcp_breakdown WHERE target_id=?", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("one row per target, found %d", n)
	}
	list := store.Breakdowns()
	if len(list) != 1 || list[0].TS != 200 || list[0].ConnectUs != 2000 {
		t.Errorf("the latest result should be the one kept: %+v", list)
	}
}

// Targets never broken down are listed with an empty result, and ICMP
// targets are not listed at all: there is no connection to time. A TCP
// target without a port cannot exist — the store refuses one — so the
// guard on the port only covers rows written by an older version.
func TestBreakdownLists(t *testing.T) {
	store := bdStore(t)
	tcpID := bdTarget(t, store, "tcp-one", "192.0.2.1", "tcp", 443)
	bdTarget(t, store, "icmp-one", "192.0.2.2", "icmp", 0)

	list := store.Breakdowns()
	if len(list) != 1 {
		t.Fatalf("only a TCP target with a port can be broken down: %+v", list)
	}
	if list[0].TargetID != tcpID || list[0].TS != 0 {
		t.Errorf("a target never measured is listed with an empty result: %+v", list[0])
	}
}

// The endpoint refuses what it cannot measure, with a reason rather than
// a bare 400.
func TestBreakdownRunRefusesICMP(t *testing.T) {
	store := bdStore(t)
	id := bdTarget(t, store, "icmp-one", "192.0.2.2", "icmp", 0)
	api := &API{store: store, token: "secret", probeID: 1}
	req := httptest.NewRequest("POST", "/api/v1/admin/breakdown/"+strconv.FormatInt(id, 10), nil)
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	rec := httptest.NewRecorder()
	api.breakdownRun(rec, req)
	if rec.Code != 400 {
		t.Fatalf("an ICMP target cannot be broken down: HTTP %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "TCP target") {
		t.Errorf("the refusal should say why: %s", rec.Body.String())
	}
}
