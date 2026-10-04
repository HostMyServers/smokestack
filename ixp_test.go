package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakePeeringDB serves the three endpoints the peering LAN table is built
// from, with enough prefixes to pass the truncation guard.
func fakePeeringDB(t *testing.T, prefixes int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/ixpfx"):
			var b strings.Builder
			b.WriteString(`{"data":[{"ixlan_id":7,"prefix":"198.51.100.0/24"}`)
			for i := 0; i < prefixes; i++ {
				fmt.Fprintf(&b, `,{"ixlan_id":7,"prefix":"2001:db8:%x::/64"}`, i)
			}
			b.WriteString(`]}`)
			w.Write([]byte(b.String()))
		case strings.HasPrefix(r.URL.Path, "/ixlan"):
			w.Write([]byte(`{"data":[{"id":7,"ix_id":3}]}`))
		case strings.HasPrefix(r.URL.Path, "/ix"):
			w.Write([]byte(`{"data":[{"id":3,"name":"TEST-IX","name_long":"Test Internet Exchange"}]}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	}))
}

// An address on a peering LAN is named by the exchange holding the prefix;
// anything else is not, and a malformed entry does not take the table down.
func TestIXTableLookup(t *testing.T) {
	tbl := buildIXTable(ixStored{Nets: map[string]string{
		"198.51.100.0/24": "TEST-IX",
		"2001:db8::/32":   "TEST-IX6",
		"not a prefix":    "ignored",
		"203.0.113.0/24":  "", // an exchange without a name names nothing
	}})
	svc := NewASNService(nil, nil, "")
	svc.setIXTable(tbl)

	cases := []struct{ addr, want string }{
		{"198.51.100.7", "TEST-IX"},
		{"2001:db8::1", "TEST-IX6"},
		{"203.0.113.1", ""},
		{"192.0.2.1", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := svc.IXAt(net.ParseIP(c.addr)); got != c.want {
			t.Errorf("IXAt(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}

// The table is fetched from PeeringDB, kept in config.db, and found again
// after a restart: naming a peering hop must not depend on PeeringDB being
// up at that moment.
func TestIXPrefixesRefreshAndReload(t *testing.T) {
	srv := fakePeeringDB(t, 600)
	defer srv.Close()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewASNService(store, nil, "")
	svc.pdbBase = srv.URL + "/"
	if !svc.ixStale() {
		t.Error("an instance without a table must consider it stale")
	}
	if err := svc.refreshIXPrefixes(); err != nil {
		t.Fatal(err)
	}
	if got := svc.IXAt(net.ParseIP("198.51.100.9")); got != "TEST-IX" {
		t.Errorf("the exchange should name the hop, got %q", got)
	}
	if svc.ixStale() {
		t.Error("a table just fetched is not stale")
	}

	restarted := NewASNService(store, nil, "")
	if got := restarted.IXAt(net.ParseIP("198.51.100.9")); got != "TEST-IX" {
		t.Errorf("the table should come back from config.db, got %q", got)
	}
}

// A truncated answer must not replace a table that works.
func TestIXPrefixesTruncated(t *testing.T) {
	srv := fakePeeringDB(t, 2)
	defer srv.Close()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewASNService(store, nil, "")
	svc.pdbBase = srv.URL + "/"
	svc.setIXTable(buildIXTable(ixStored{At: time.Now().Unix(),
		Nets: map[string]string{"198.51.100.0/24": "TEST-IX"}}))

	if err := svc.refreshIXPrefixes(); err == nil {
		t.Fatal("three peering LANs is not a table")
	}
	if got := svc.IXAt(net.ParseIP("198.51.100.9")); got != "TEST-IX" {
		t.Errorf("the previous table must survive a bad answer, got %q", got)
	}
	if store.Setting(ixPrefixesKey, "") != "" {
		t.Error("a truncated table must not be written to config.db")
	}
}

// The hop a probe could not name: an address on a peering LAN carries the
// exchange, and the autonomous system comes from what its member declares.
func TestHopFillerNamesPeeringLAN(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetSetting(ixMemberKey+"198.51.100.7", "AS64501")
	store.SetSetting("ipasn:203.0.113.9", "AS64502")

	svc := NewASNService(store, nil, "")
	svc.setIXTable(buildIXTable(ixStored{At: time.Now().Unix(),
		Nets: map[string]string{"198.51.100.0/24": "TEST-IX"}}))

	hops := []Hop{
		{TTL: 1, Addr: "192.168.0.1"},
		{TTL: 2, Addr: "198.51.100.7"},
		{TTL: 3, Addr: "203.0.113.9"},
		{TTL: 4, Addr: "203.0.113.10", ASN: "AS64503"},
		{TTL: 5},
	}
	svc.HopFiller()(hops)

	if hops[0].ASN != "" || hops[0].IX != "" {
		t.Errorf("a private address is named by nobody: %+v", hops[0])
	}
	if hops[1].IX != "TEST-IX" || hops[1].ASN != "AS64501" {
		t.Errorf("the peering hop should carry both the exchange and its member: %+v", hops[1])
	}
	if hops[2].ASN != "AS64502" || hops[2].IX != "" {
		t.Errorf("an ordinary hop takes its AS from the routing table: %+v", hops[2])
	}
	if hops[3].ASN != "AS64503" {
		t.Errorf("an AS the probe resolved must not be overwritten: %+v", hops[3])
	}
}

// An exchange named on a hop belongs to the link entering that network: the
// map draws it on the arrow, so it has to come out of the sequence.
func TestASSeqCarriesExchange(t *testing.T) {
	tr := &Traceroute{Reached: true, Hops: []Hop{
		hopAS("192.0.2.1", "AS64500"),
		{Addr: "198.51.100.7", ASN: "AS64501", IX: "TEST-IX", RTTms: []float64{6}, Sent: 3},
		hopAS("203.0.113.5", "AS64502"),
	}}
	_, _, ix := asSeq(tr, "AS64500", "AS64502")
	if ix["AS64500>AS64501"] != "TEST-IX" {
		t.Errorf("the link into the peer should name the exchange: %v", ix)
	}
	if len(ix) != 1 {
		t.Errorf("only the link that crossed one carries it: %v", ix)
	}
	// Same hop, same claim, on the chain rather than the map.
	if got := ixEntering(tr, "AS64501"); got != "TEST-IX" {
		t.Errorf("ixEntering = %q", got)
	}
	if got := ixEntering(tr, "AS64502"); got != "" {
		t.Errorf("a network entered over transit crosses no exchange: %q", got)
	}
}

// A resolver that did not answer said nothing about the address. Only a name
// that does not exist is an answer, and only an answer is cached.
func TestDNSMiss(t *testing.T) {
	if !dnsMiss(&net.DNSError{Err: "no such host", IsNotFound: true}) {
		t.Error("NXDOMAIN is an answer")
	}
	if dnsMiss(&net.DNSError{Err: "i/o timeout", IsTimeout: true}) {
		t.Error("a timeout must not be cached as an absence")
	}
	if dnsMiss(errors.New("broken")) {
		t.Error("an unknown failure is not an answer either")
	}
}

// End to end: a traceroute crossing a peering LAN comes back named on both
// surfaces that show one — the hop table and the route to the target.
func TestRouteNamesPeeringHop(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	site := store.Site()
	site.ASN = "AS64500"
	site.Org = "Us"
	site.PublicTraceroutes = true
	store.SetSite(site)
	cat, _ := store.CreateCategory("c", "C", "C", true)
	id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "t", Title: "T",
		Host: "203.0.113.9", PinIP: "203.0.113.9", Proto: "icmp", IntervalS: 60,
		Packets: 10, SpacingMs: 100, TimeoutMs: 1000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Everything the handler would otherwise fetch from a third party is
	// already in the cache, so the test needs nothing but itself.
	now := time.Now().Unix()
	store.SetSetting("ipasn:203.0.113.9", "AS64502")
	store.SetSetting(ixMemberKey+"198.51.100.7", "AS64501")
	store.SetSetting("ris:203.0.113.9",
		fmt.Sprintf(`{"prefix":"203.0.113.0/24","origin_asn":"AS64502","fetched_at":%d}`, now))
	for asn, holder := range map[string]string{"64501": "Peer", "64502": "Them"} {
		store.SetSetting("asn:"+asn,
			fmt.Sprintf(`{"asn":%q,"holder":%q,"fetched_at":%d}`, asn, holder, now))
	}
	if err := store.SaveTraceroute(&Traceroute{TargetID: id, ProbeID: 1, TS: now,
		Kind: "reference", Family: 4, Dest: "203.0.113.9", Reached: true, Hops: []Hop{
			hopAS("192.0.2.1", "AS64500"),
			{TTL: 2, Addr: "198.51.100.7", RTTms: []float64{6.2}, Sent: 3},
			hopAS("203.0.113.9", "AS64502")}}); err != nil {
		t.Fatal(err)
	}

	svc := NewASNService(store, nil, "")
	svc.setIXTable(buildIXTable(ixStored{At: now,
		Nets: map[string]string{"198.51.100.0/24": "TEST-IX"}}))
	api := &API{store: store, asn: svc}

	w := httptest.NewRecorder()
	api.asPathView(w, httptest.NewRequest("GET", fmt.Sprintf("/api/v1/aspath?target=%d", id), nil))
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body)
	}
	var route ASRoute
	if err := json.Unmarshal(w.Body.Bytes(), &route); err != nil {
		t.Fatal(err)
	}
	if len(route.Path) != 1 || route.Path[0].ASN != "AS64501" {
		t.Fatalf("the peer crossed at the exchange belongs to the route: %+v", route.Path)
	}
	if route.Path[0].Via != "TEST-IX" {
		t.Errorf("the route should name the exchange it crossed: %+v", route.Path[0])
	}
	if route.Path[0].Name != "Peer" {
		t.Errorf("and name that network: %+v", route.Path[0])
	}
	found := false
	for _, e := range route.Graph.Edges {
		if e.From == "AS64500" && e.To == "AS64501" {
			found = e.IX == "TEST-IX"
		}
	}
	if !found {
		t.Errorf("the map should draw the exchange on the link: %+v", route.Graph.Edges)
	}

	w = httptest.NewRecorder()
	api.traceroutesPublic(w, httptest.NewRequest("GET",
		fmt.Sprintf("/api/v1/traceroutes?target=%d", id), nil))
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body)
	}
	var list []*Traceroute
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("one traceroute expected, got %d", len(list))
	}
	if h := list[0].Hops[1]; h.ASN != "AS64501" || h.IX != "TEST-IX" {
		t.Errorf("the hop table should name the peering hop: %+v", h)
	}
}
