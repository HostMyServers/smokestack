package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func hopAS(addr, as string) Hop { return Hop{Addr: addr, ASN: as, Sent: 3, RTTms: []float64{1}} }

// Comparing AS paths, not addresses: two parallel links of the same operator
// give different addresses and the same path, and must not raise anything.
func TestASPathComparison(t *testing.T) {
	a := &Traceroute{Hops: []Hop{hopAS("192.0.2.1", "AS64500"), hopAS("198.51.100.1", "AS174"),
		hopAS("198.51.100.9", "AS174"), hopAS("203.0.113.5", "AS15169")}}
	b := &Traceroute{Hops: []Hop{hopAS("192.0.2.1", "AS64500"), hopAS("198.51.100.77", "AS174"),
		hopAS("203.0.113.5", "AS15169")}}
	if got := strings.Join(asPath(a), " "); got != "AS64500 AS174 AS15169" {
		t.Errorf("repetitions must collapse: %q", got)
	}
	if !samePath(asPath(a), asPath(b)) {
		t.Error("a different address inside the same AS is not a path change")
	}
	c := &Traceroute{Hops: []Hop{hopAS("192.0.2.1", "AS64500"), hopAS("198.51.100.1", "AS3356"),
		hopAS("203.0.113.5", "AS15169")}}
	if samePath(asPath(a), asPath(c)) {
		t.Error("a transit change must be seen")
	}
	// Silent hops carry no AS and must not break the comparison.
	d := &Traceroute{Hops: []Hop{hopAS("192.0.2.1", "AS64500"), hopAS("*", ""),
		hopAS("198.51.100.1", "AS174"), hopAS("203.0.113.5", "AS15169")}}
	if !samePath(asPath(a), asPath(d)) {
		t.Error("a silent hop is not a path change")
	}
}

// A change between two healthy paths is recorded as an event, once, and is
// available to the alerting as context.
func TestPathChangeRecorded(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("c", "C", "C", true)
	id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "t", Title: "Transit Paris",
		Host: "192.0.2.9", Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100,
		TimeoutMs: 1000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	ref := func(ts int64, transit string) {
		if err := store.SaveTraceroute(&Traceroute{TargetID: id, ProbeID: 1, TS: ts,
			Kind: "reference", Reason: "healthy path", Family: 4, Dest: "192.0.2.9", Reached: true,
			Hops: []Hop{hopAS("192.0.2.1", "AS64500"), hopAS("198.51.100.1", transit),
				hopAS("203.0.113.5", "AS15169")}}); err != nil {
			t.Fatal(err)
		}
	}
	ref(now-2*86400, "AS174")
	if _, ok := store.RecentPathChange(id, now-7*86400); ok {
		t.Error("a first reference cannot be a change")
	}
	ref(now-86400, "AS174")
	if _, ok := store.RecentPathChange(id, now-7*86400); ok {
		t.Error("an identical path is not a change")
	}
	ref(now-3600, "AS3356") // the transit provider changed
	detail, ok := store.RecentPathChange(id, now-7*86400)
	if !ok || !strings.Contains(detail, "AS174") || !strings.Contains(detail, "AS3356") {
		t.Fatalf("the change should be recorded with both paths: %q", detail)
	}
	// The title drawn on the graph stays short; the body names both ends of
	// the path, because a route is only meaningful between two networks.
	var title, body, scope string
	var scopeID *int64
	store.cfg.QueryRow(`SELECT title,COALESCE(body,''),scope,scope_id FROM events
	                    WHERE kind='path' ORDER BY ts_start DESC LIMIT 1`).
		Scan(&title, &body, &scope, &scopeID)
	if len(title) > 60 || !strings.Contains(strings.ToLower(title), "route changed") {
		t.Errorf("the event title must stay short and readable: %q", title)
	}
	// The event belongs to this target and to nothing else.
	if scope != "target" || scopeID == nil || *scopeID != id {
		t.Errorf("the event must be scoped to its target: scope=%q id=%v", scope, scopeID)
	}
	if !strings.Contains(body, "AS174") || !strings.Contains(body, "AS3356") {
		t.Errorf("the paths must be in the body: %q", body)
	}
	if !strings.Contains(body, "AS15169") || !strings.Contains(body, "Transit Paris") {
		t.Errorf("the body must name the far end of the path: %q", body)
	}
	if !strings.Contains(body, "192.0.2.9") {
		t.Errorf("the body must name the address actually measured: %q", body)
	}
	if !strings.Contains(detail, "Transit Paris") {
		t.Errorf("the context given to alerting should name the target: %q", detail)
	}
	// Out of the window, it is not offered as context any more.
	if _, ok := store.RecentPathChange(id, now-60); ok {
		t.Error("an old change must not be attached to a fresh incident")
	}
}

// The target's own interval wins over the instance default.
func TestReferenceIntervalPerTarget(t *testing.T) {
	d := newDetector(TracerouteConfig{PerHour: 30, ReferenceHours: 24})
	base := time.Now()
	d.now = func() time.Time { return base }
	good := Measurement{Sent: 10, Lost: 0, RTTus: []float64{1000, 1100, 1050, 1080, 1020}}
	for i := 0; i < 6; i++ {
		d.observeTarget(1, good, 4)
		d.observeTarget(2, good, 0)
	}
	// Five hours later: the target asking for four hours is due, the other
	// one, on the instance default of 24 h, is not.
	base = base.Add(5 * time.Hour)
	if kind, _ := d.observeTarget(1, good, 4); kind != "reference" {
		t.Errorf("a target asking for 4 h should be due: %q", kind)
	}
	if kind, _ := d.observeTarget(2, good, 0); kind == "reference" {
		t.Error("a target on the 24 h default should not be due after 5 h")
	}
}

// PeeringDB contacts: the NOC role comes first, contacts without any way to
// reach them are dropped, and a network declaring none is not an error.
// newASNServiceFor points the service at a fake PeeringDB.
func newASNServiceFor(base string) *ASNService {
	svc := NewASNService(nil, nil, "")
	svc.pdbBase = base
	return svc
}

func TestPeeringDBContactOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/net"):
			w.Write([]byte(`{"data":[{"id":42,"name":"Example Transit","asn":174,
			  "policy_general":"Open","website":"https://example.net"}]}`))
		case strings.HasPrefix(r.URL.Path, "/poc"):
			w.Write([]byte(`{"data":[
			  {"role":"Policy","name":"Peering","email":"peering@example.net","status":"ok"},
			  {"role":"NOC","name":"NOC 24/7","email":"noc@example.net","phone":"+33100000000","status":"ok"},
			  {"role":"Technical","name":"Nobody","email":"","phone":"","status":"ok"},
			  {"role":"Abuse","name":"Abuse","email":"abuse@example.net","status":"deleted"}]}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()
	svc := newASNServiceFor(srv.URL + "/")
	info := &ASNInfo{ASN: "AS174"}
	p := svc.fetchPeeringDB("174", info)
	if p == nil {
		t.Fatal("a PeeringDB record was expected")
	}
	if len(p.Contacts) != 2 {
		t.Fatalf("2 reachable contacts expected, got %+v", p.Contacts)
	}
	if p.Contacts[0].Role != "NOC" {
		t.Errorf("the NOC must come first, got %q", p.Contacts[0].Role)
	}
	if p.Contacts[0].Phone == "" || p.Contacts[0].Email == "" {
		t.Errorf("the NOC contact lost its details: %+v", p.Contacts[0])
	}
	for _, c := range p.Contacts {
		if c.Email == "" && c.Phone == "" {
			t.Error("a contact with no way to reach it must be dropped")
		}
	}
}

// The middle of the route comes from the traceroute, with the two ends left
// out — they are our AS and the destination's, added by the handler — and a
// gap reported when the path went silent before arriving.
func TestASRouteFrom(t *testing.T) {
	full := &Traceroute{Reached: true, Hops: []Hop{
		hopAS("192.0.2.1", "AS64500"), hopAS("192.0.2.2", "AS64500"),
		hopAS("198.51.100.1", "AS174"), hopAS("198.51.100.9", "AS174"),
		hopAS("203.0.113.5", "AS29222")}}
	mid, gap := asRouteFrom(full, "AS64500", "AS29222")
	if len(mid) != 1 || mid[0].ASN != "AS174" || mid[0].Hops != 2 {
		t.Errorf("only the transit belongs in the middle: %+v", mid)
	}
	if gap {
		t.Error("a traceroute reaching the destination has no gap")
	}
	// Silent last hops: the segment before the destination is unknown.
	silent := &Traceroute{Reached: false, Hops: []Hop{
		hopAS("192.0.2.1", "AS64500"), hopAS("198.51.100.1", "AS174"),
		hopAS("*", ""), hopAS("*", "")}}
	mid, gap = asRouteFrom(silent, "AS64500", "AS29222")
	if len(mid) != 1 || mid[0].ASN != "AS174" {
		t.Errorf("middle: %+v", mid)
	}
	if !gap {
		t.Error("silent hops before the destination must be reported as a gap")
	}
	// A first hop in private space carries no AS: our own AS must not
	// depend on it, which is why the handler adds it.
	priv := &Traceroute{Reached: true, Hops: []Hop{
		hopAS("10.0.0.1", ""), hopAS("198.51.100.1", "AS3356"), hopAS("203.0.113.5", "AS29222")}}
	mid, gap = asRouteFrom(priv, "AS64500", "AS29222")
	if len(mid) != 1 || mid[0].ASN != "AS3356" || gap {
		t.Errorf("private first hop mishandled: %+v gap=%v", mid, gap)
	}
	// A traceroute whose hops carry no AS at all leaves the middle empty
	// and the link unknown, rather than pretending the two ends touch.
	blind := &Traceroute{Reached: false, Hops: []Hop{hopAS("*", ""), hopAS("*", "")}}
	mid, gap = asRouteFrom(blind, "AS64500", "AS29222")
	if len(mid) != 0 || !gap {
		t.Errorf("a blind traceroute must give an explicit gap: %+v gap=%v", mid, gap)
	}
}

// The graph is built from several traceroutes: it must show both transits
// when the target was reached through each, mark the current path, and keep
// an unmeasured stretch as an explicit break.
func TestBuildASGraph(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("c", "C", "C", true)
	id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "t", Title: "T", Host: "192.0.2.9",
		Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100, TimeoutMs: 1000,
		Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	save := func(ts int64, reached bool, hops ...Hop) {
		if err := store.SaveTraceroute(&Traceroute{TargetID: id, ProbeID: 1, TS: ts,
			Kind: "reference", Family: 4, Dest: "192.0.2.9", Reached: reached, Hops: hops}); err != nil {
			t.Fatal(err)
		}
	}
	// Older: through AS174. Newer: through AS3356, and it is the current one.
	save(now-7200, true, hopAS("198.51.100.1", "AS174"), hopAS("203.0.113.5", "AS29222"))
	save(now-3600, true, hopAS("198.51.100.1", "AS174"), hopAS("203.0.113.5", "AS29222"))
	save(now-60, true, hopAS("198.51.100.9", "AS3356"), hopAS("203.0.113.5", "AS29222"))

	g := store.BuildASGraph(id, "AS64500", "AS29222", 25)
	if g == nil {
		t.Fatal("a graph was expected")
	}
	if g.Traces != 3 {
		t.Errorf("3 traceroutes expected, got %d", g.Traces)
	}
	byASN := map[string]ASGraphNode{}
	for _, n := range g.Nodes {
		byASN[n.ASN] = n
	}
	for _, want := range []string{"AS64500", "AS174", "AS3356", "AS29222"} {
		if _, ok := byASN[want]; !ok {
			t.Errorf("%s missing from the graph", want)
		}
	}
	if !byASN["AS64500"].Origin || !byASN["AS29222"].Dest {
		t.Error("the two ends must be marked as such")
	}
	if byASN["AS64500"].Layer != 0 {
		t.Errorf("our AS starts the graph: layer %d", byASN["AS64500"].Layer)
	}
	if byASN["AS174"].Seen != 2 || byASN["AS3356"].Seen != 1 {
		t.Errorf("how often each transit was seen: %d and %d", byASN["AS174"].Seen, byASN["AS3356"].Seen)
	}
	var currentTransit string
	for _, e := range g.Edges {
		if e.Current && e.From == "AS64500" {
			currentTransit = e.To
		}
	}
	if currentTransit != "AS3356" {
		t.Errorf("the current path should go through AS3356, got %q", currentTransit)
	}
	// A traceroute that stops answering keeps an explicit break.
	save(now-30, false, hopAS("198.51.100.9", "AS3356"), Hop{Addr: "*", Sent: 3})
	g = store.BuildASGraph(id, "AS64500", "AS29222", 25)
	if !g.Incomplete {
		t.Error("an unmeasured stretch must be reported")
	}
	found := false
	for _, n := range g.Nodes {
		if n.Unknown {
			found = true
		}
	}
	if !found {
		t.Error("the break must appear as a node in the graph")
	}
}

// The RIS view is parsed from what RIPEstat returns: the prefix, its origin,
// and the upstreams the collectors see in front of it — ranked by how many
// peers saw each, with prepended AS ignored.
func TestRefreshRIS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "network-info"):
			w.Write([]byte(`{"data":{"prefix":"185.31.40.0/22","asns":[{"asn":29222}]}}`))
		case strings.Contains(r.URL.Path, "looking-glass"):
			w.Write([]byte(`{"data":{"rrcs":[
			  {"peers":[{"as_path":"1299 3356 29222"},{"as_path":"6939 174 29222"},
			            {"as_path":"20932 3356 29222"},{"as_path":"3333 29222 29222"}]}]}}`))
		default:
			w.Write([]byte(`{"data":{}}`))
		}
	}))
	defer srv.Close()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewASNService(store, nil, "")
	oldBase := ripestatBaseForTests
	ripestatBaseForTests = srv.URL + "/"
	defer func() { ripestatBaseForTests = oldBase }()

	svc.RefreshRIS("185.31.40.1")
	v, ok := svc.RISFor("185.31.40.1")
	if !ok {
		t.Fatal("the view should have been cached")
	}
	if v.Prefix != "185.31.40.0/22" || v.OriginASN != "AS29222" {
		t.Errorf("prefix and origin: %+v", v)
	}
	if !v.Announced || v.Peers != 4 {
		t.Errorf("four collector peers expected: %+v", v)
	}
	if len(v.Upstreams) == 0 || v.Upstreams[0] != "AS3356" {
		t.Errorf("the most seen upstream should come first: %v", v.Upstreams)
	}
	// A path ending "29222 29222" is prepending, not an upstream.
	for _, up := range v.Upstreams {
		if up == "AS29222" {
			t.Error("the origin must not be listed as its own upstream")
		}
	}
}

// A route event belongs to one target. It must appear on that target's page
// and on no other, which is what the scoped events endpoint decides.
func TestRouteEventsAreServedPerTarget(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("c", "C", "C", true)
	mk := func(slug, title string) int64 {
		id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: slug, Title: title,
			Host: "192.0.2.9", Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100,
			TimeoutMs: 1000, Public: true, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	netflix, tv := mk("netflix", "Netflix"), mk("france-tv", "France TV")
	now := time.Now().Unix()
	store.cfg.Exec(`INSERT INTO events(ts_start,kind,title,body,scope,scope_id,public)
	                VALUES(?,'path','Route changed','to Netflix','target',?,1)`, now-60, netflix)
	store.cfg.Exec(`INSERT INTO events(ts_start,kind,title,body,scope,scope_id,public)
	                VALUES(?,'path','Route changed','to France TV','target',?,1)`, now-60, tv)
	store.cfg.Exec(`INSERT INTO events(ts_start,kind,title,scope,public)
	                VALUES(?,'maintenance','Instance maintenance','global',1)`, now-60)

	api := &API{store: store}
	get := func(q string) []Event {
		w := httptest.NewRecorder()
		api.events(w, httptest.NewRequest("GET", "/api/v1/events?"+q, nil))
		if w.Code != 200 {
			t.Fatalf("HTTP %d for %q: %s", w.Code, q, w.Body)
		}
		var out []Event
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	bodies := func(evs []Event) string {
		var b []string
		for _, e := range evs {
			b = append(b, e.Title+"/"+e.Body)
		}
		return strings.Join(b, "|")
	}

	// Netflix's page: its own route change, plus what concerns the instance.
	got := bodies(get("target=netflix"))
	if !strings.Contains(got, "to Netflix") {
		t.Errorf("the target's own route change is missing: %q", got)
	}
	if strings.Contains(got, "to France TV") {
		t.Errorf("another target's route change must not appear here: %q", got)
	}
	if !strings.Contains(got, "Instance maintenance") {
		t.Errorf("an instance-wide event still belongs on every page: %q", got)
	}
	// By numeric identifier too, which is what the page actually sends.
	if g := bodies(get(fmt.Sprintf("target=%d", tv))); !strings.Contains(g, "to France TV") ||
		strings.Contains(g, "to Netflix") {
		t.Errorf("lookup by id: %q", g)
	}
	// Without a target — the home page — no route event at all.
	if g := bodies(get("")); strings.Contains(g, "Route changed") {
		t.Errorf("a route event concerns one path, not the instance: %q", g)
	}
	// An unknown target is an error, not an empty list that could be mistaken
	// for "this target has no events".
	w := httptest.NewRecorder()
	api.events(w, httptest.NewRequest("GET", "/api/v1/events?target=nope", nil))
	if w.Code != 404 {
		t.Errorf("an unknown target should answer 404, got %d", w.Code)
	}
}

// A rotating name answers from a different machine at each pass. The two
// reference traceroutes then went to different places, so the AS path differs
// without anything having been rerouted: that is not a route change, and the
// route map must not mix the paths either.
func TestRotatingTargetIsNotARouteChange(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("c", "C", "C", true)
	id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "pool", Title: "NTP pool",
		Host: "fr.pool.ntp.org", Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100,
		TimeoutMs: 1000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	save := func(ts int64, dest, transit, last string) {
		if err := store.SaveTraceroute(&Traceroute{TargetID: id, ProbeID: 1, TS: ts,
			Kind: "reference", Family: 4, Dest: dest, Reached: true,
			Hops: []Hop{hopAS("192.0.2.1", "AS64500"), hopAS("198.51.100.1", transit),
				hopAS(dest, last)}}); err != nil {
			t.Fatal(err)
		}
	}
	// Two references, two different servers of the pool, two different paths.
	save(now-7200, "203.0.113.10", "AS174", "AS2200")
	save(now-3600, "203.0.113.77", "AS3356", "AS1234")
	if detail, ok := store.RecentPathChange(id, now-86400); ok {
		t.Errorf("a different server is not a route change: %q", detail)
	}
	// The same server, a real transit change: that one is recorded.
	save(now-1800, "203.0.113.77", "AS174", "AS1234")
	if _, ok := store.RecentPathChange(id, now-86400); !ok {
		t.Error("a genuine change on the same address must still be seen")
	}
	// The map keeps one address and says how many traceroutes it left out.
	g := store.BuildASGraph(id, "AS64500", "AS1234", 25)
	if g == nil {
		t.Fatal("a graph was expected")
	}
	if g.OtherAddrs != 1 {
		t.Errorf("one traceroute went to another address: OtherAddrs=%d", g.OtherAddrs)
	}
	for _, n := range g.Nodes {
		if n.ASN == "AS2200" {
			t.Error("the path to another server must not appear in this map")
		}
	}
}

// A category emptied by the operator must be deletable. Deleting a target
// archives it so its history survives, and those archived rows used to be
// counted as live ones: the category could never be removed, and the message
// pointed at targets the operator could no longer see.
func TestDeleteCategoryIgnoresArchivedTargets(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, err := store.CreateCategory("resolvers", "Résolveurs", "Resolvers", true)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, slug := range []string{"quad9", "cloudflare", "google"} {
		id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: slug, Title: slug,
			Host: "192.0.2.9", Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100,
			TimeoutMs: 1000, Public: true, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := store.DeleteCategory(cat); err == nil {
		t.Fatal("a category holding live targets must not be deleted")
	}
	for _, id := range ids {
		if err := store.ArchiveTarget(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteCategory(cat); err != nil {
		t.Fatalf("once every target is archived the category must go: %v", err)
	}
	// The history survives. targets.category_id carries ON DELETE CASCADE,
	// so an archived target left in the deleted category would have been
	// deleted with it, measurements included.
	arch, err := store.ArchivedTargets()
	if err != nil || len(arch) != 3 {
		t.Fatalf("the three archived targets must remain: %d, %v", len(arch), err)
	}
	holder, err := store.archiveCategoryID()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arch {
		if a.CategoryID != holder {
			t.Errorf("%s should have moved to the archive category, it is in %d",
				a.Slug, a.CategoryID)
		}
	}
	// The archive itself is not deletable while it holds history: doing so
	// would destroy exactly what it exists to keep.
	if err := store.DeleteCategory(holder); err == nil {
		t.Error("deleting the archive category must be refused while it holds targets")
	}
	if got, _ := store.ArchivedTargets(); len(got) != 3 {
		t.Errorf("the refused deletion must change nothing: %d left", len(got))
	}
	// Once purged, it goes like any other.
	for _, a := range arch {
		if err := store.PurgeTarget(a.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteCategory(holder); err != nil {
		t.Errorf("an empty archive category must be deletable: %v", err)
	}
}

// Foreign keys are enforced, which is what makes the archive category
// necessary rather than decorative. A test suite running without them would
// be more permissive than production and would hide the cascade.
func TestForeignKeysAreEnforced(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var on int
	if err := store.cfg.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Fatal("foreign keys must be on: targets cascade from their category")
	}
	cat, _ := store.CreateCategory("c", "C", "C", true)
	if _, err := store.CreateTarget(&Target{CategoryID: cat + 999, Slug: "orphan",
		Title: "orphan", Host: "192.0.2.9", Proto: "icmp", IntervalS: 60, Packets: 10,
		SpacingMs: 100, TimeoutMs: 1000, Enabled: true}); err == nil {
		t.Error("a target in a category that does not exist must be refused")
	}
}

// Thresholds are per target, and rejected when they would make the states
// incoherent.
func TestTargetThresholdValidation(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("c", "C", "C", true)
	mk := func(slug string, warn, crit, factor float64) error {
		_, err := store.CreateTarget(&Target{CategoryID: cat, Slug: slug, Title: slug,
			Host: "192.0.2.9", Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100,
			TimeoutMs: 1000, Public: true, Enabled: true,
			LossWarn: warn, LossCrit: crit, LatFactor: factor})
		return err
	}
	if err := mk("ok", 8, 20, 3); err != nil {
		t.Fatalf("a plausible set must be accepted: %v", err)
	}
	if err := mk("backwards", 20, 8, 0); err == nil {
		t.Error("a critical threshold below the warning one must be refused")
	}
	if err := mk("over", 0, 140, 0); err == nil {
		t.Error("a loss threshold above 100 %% must be refused")
	}
	if err := mk("tiny-factor", 0, 0, 1.01); err == nil {
		t.Error("a latency factor barely above 1 must be refused")
	}
	if err := mk("defaults", 0, 0, 0); err != nil {
		t.Errorf("zero means instance default and must be accepted: %v", err)
	}
	// Stored and read back.
	got, err := store.TargetBySlug("ok")
	if err != nil {
		t.Fatal(err)
	}
	if got.LossWarn != 8 || got.LossCrit != 20 || got.LatFactor != 3 {
		t.Errorf("thresholds not persisted: %+v", got)
	}
}

// The Forwarded header of RFC 7239 is read in preference to the older
// X-Forwarded-For, and in both only the element the proxy vouches for counts.
func TestClientIPHeaders(t *testing.T) {
	req := func(remote string, h map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name, remote string
		headers      map[string]string
		want         string
	}{
		{"direct connection, headers ignored", "198.51.100.7:4242",
			map[string]string{"Forwarded": "for=203.0.113.1"}, "198.51.100.7"},
		{"Forwarded from a local proxy", "127.0.0.1:5555",
			map[string]string{"Forwarded": `for=203.0.113.1;proto=https`}, "203.0.113.1"},
		{"Forwarded wins over the legacy header", "127.0.0.1:5555",
			map[string]string{"Forwarded": "for=203.0.113.1",
				"X-Forwarded-For": "198.51.100.9"}, "203.0.113.1"},
		{"a client-supplied element is pushed left and ignored", "127.0.0.1:5555",
			map[string]string{"Forwarded": `for="1.2.3.4", for=203.0.113.1`}, "203.0.113.1"},
		{"quoted IPv6 with a port", "127.0.0.1:5555",
			map[string]string{"Forwarded": `for="[2001:db8::1]:4711"`}, "2001:db8::1"},
		{"obfuscated identifiers are not addresses", "127.0.0.1:5555",
			map[string]string{"Forwarded": "for=_hidden, for=unknown"}, "127.0.0.1"},
		{"the legacy header still works", "127.0.0.1:5555",
			map[string]string{"X-Forwarded-For": "1.2.3.4, 203.0.113.1"}, "203.0.113.1"},
		{"nothing at all", "127.0.0.1:5555", nil, "127.0.0.1"},
	}
	for _, c := range cases {
		if got := clientIP(req(c.remote, c.headers)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A traceroute that reached its destination but attributed no hop to an AS
// used to draw the two ends of the route touching, which claims the two
// networks are neighbours. An unknown middle must be said, not implied.
func TestRouteWithoutAnyASIsAGap(t *testing.T) {
	// Reached, three hops, none carrying an AS: private addressing, silent
	// routers, or lookups that have not answered yet.
	blind := &Traceroute{Reached: true, Hops: []Hop{
		hopAS("10.0.0.1", ""), hopAS("10.0.0.2", ""), hopAS("203.0.113.5", "")}}
	mid, gap := asRouteFrom(blind, "AS64500", "AS29222")
	if len(mid) != 0 {
		t.Errorf("no AS could be attributed, the middle must stay empty: %+v", mid)
	}
	if !gap {
		t.Error("an unknown middle must be reported as a gap, not drawn as adjacency")
	}
	// A traceroute with no hops at all is a different case: nothing ran.
	if _, gap := asRouteFrom(&Traceroute{Reached: true}, "AS64500", "AS29222"); gap {
		t.Error("an empty traceroute is not a gap in a measured path")
	}
}

// The route endpoint says why its middle is empty, rather than leaving the
// reader to conclude that the two ends are neighbours.
func TestRouteSaysWhyItIsEmpty(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	site := store.Site()
	site.ASN = "AS64500"
	site.PublicTraceroutes = true
	store.SetSite(site)
	cat, _ := store.CreateCategory("c", "C", "C", true)
	id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "t", Title: "T",
		Host: "192.0.2.9", Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100,
		TimeoutMs: 1000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	api := &API{store: store, asn: NewASNService(store, nil, "")}
	get := func() ASRoute {
		w := httptest.NewRecorder()
		api.asPathView(w, httptest.NewRequest("GET",
			fmt.Sprintf("/api/v1/aspath?target=%d", id), nil))
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body)
		}
		var v ASRoute
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// A target added a minute ago has no traceroute: references are taken
	// once a day, anomalies only when something degrades.
	if v := get(); v.Empty != "no-trace" {
		t.Errorf("a target without any traceroute should say so, got %q", v.Empty)
	}
	// One that ran but revealed no AS says something different.
	if err := store.SaveTraceroute(&Traceroute{TargetID: id, ProbeID: 1,
		TS: time.Now().Unix(), Kind: "reference", Family: 4, Dest: "192.0.2.9",
		Reached: true, Hops: []Hop{hopAS("10.0.0.1", ""), hopAS("192.0.2.9", "")}}); err != nil {
		t.Fatal(err)
	}
	v := get()
	if v.Empty != "no-as" {
		t.Errorf("a traceroute without any AS should say so, got %q", v.Empty)
	}
	if !v.Gap {
		t.Error("and it must carry the gap, so the two ends are not drawn touching")
	}
}

// IPv4 and IPv6 cross different networks, so the pair is two targets to
// compare, never one series to average. Creating the twin copies the
// settings, states the family on both sides, and the two find each other.
func TestTwinTarget(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("c", "C", "C", true)
	id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "netflix", Title: "Netflix",
		Host: "www.netflix.com", Proto: "icmp", IntervalS: 30, Packets: 10, SpacingMs: 200,
		TimeoutMs: 1000, Public: true, Enabled: true, LossWarn: 5, KeepDays: 90})
	if err != nil {
		t.Fatal(err)
	}
	// The name has an AAAA record, so the undecided target keeps the bare
	// name in IPv6 and the twin takes the legacy family with the suffix.
	old := lookupIPv6
	lookupIPv6 = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("2001:db8::1")}, nil }
	defer func() { lookupIPv6 = old }()

	twin, err := store.CreateTwin(id)
	if err != nil {
		t.Fatalf("creating the twin: %v", err)
	}
	if twin.Family != 4 {
		t.Errorf("the twin must take the legacy family, got IPv%d", twin.Family)
	}
	if twin.Slug != "netflix-v4" || twin.Title != "Netflix (IPv4)" {
		t.Errorf("slug and title: %q / %q", twin.Slug, twin.Title)
	}
	// The settings are the same, which is the point: the two series are
	// comparable only if they were measured the same way.
	if twin.IntervalS != 30 || twin.Packets != 10 || twin.SpacingMs != 200 ||
		twin.LossWarn != 5 || twin.KeepDays != 90 || !twin.Public {
		t.Errorf("settings were not carried over: %+v", twin)
	}
	// A target left on automatic now states IPv6: nothing deliberate was
	// overridden, and the bare name goes to the family that is not legacy.
	orig, _ := store.TargetByID(id)
	if orig.Family != 6 {
		t.Errorf("the original should now state IPv6, got %d", orig.Family)
	}
	// Each one finds the other, by what it measures rather than by a name.
	back, err := store.FindTwin(twin)
	if err != nil || back.ID != id {
		t.Errorf("the twin should point back at the original: %v", err)
	}
	fwd, err := store.FindTwin(orig)
	if err != nil || fwd.ID != twin.ID {
		t.Errorf("the original should find its twin: %v", err)
	}
	// Twice is refused, with the existing one named.
	if _, err := store.CreateTwin(id); err == nil {
		t.Error("a second twin must be refused")
	}
	// A literal address exists in one family only.
	lit, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "quad9", Title: "Quad9",
		Host: "9.9.9.9", Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100,
		TimeoutMs: 1000, Family: 4, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTwin(lit); err == nil {
		t.Error("a literal address cannot have a twin in the other family")
	}
	// A name with no AAAA has no pair to build, and says so rather than
	// creating a target that can only fail.
	lookupIPv6 = func(string) ([]net.IP, error) { return nil, nil }
	v4only, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "legacy", Title: "Legacy",
		Host: "v4only.example.net", Proto: "icmp", IntervalS: 60, Packets: 10, SpacingMs: 100,
		TimeoutMs: 1000, Public: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTwin(v4only); err == nil {
		t.Error("a name without an AAAA record has no IPv6 half")
	}
	if got := mustTarget(t, store, v4only); got.Family != 0 {
		t.Errorf("a refused pair must leave the target as it was, got family %d", got.Family)
	}
	lookupIPv6 = func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("2001:db8::1")}, nil }
	// A pair built by hand is recognised too: same host, proto and port.
	h4, _ := store.CreateTarget(&Target{CategoryID: cat, Slug: "a4", Title: "A4",
		Host: "example.net", Proto: "tcp", Port: 443, Family: 4, IntervalS: 60, Packets: 5,
		SpacingMs: 500, TimeoutMs: 2000, Public: true, Enabled: true})
	h6, _ := store.CreateTarget(&Target{CategoryID: cat, Slug: "a6", Title: "A6",
		Host: "example.net", Proto: "tcp", Port: 443, Family: 6, IntervalS: 60, Packets: 5,
		SpacingMs: 500, TimeoutMs: 2000, Public: true, Enabled: true})
	got, err := store.FindTwin(mustTarget(t, store, h4))
	if err != nil || got.ID != h6 {
		t.Errorf("a hand-made pair must be recognised: %v", err)
	}
	// A different port is a different service, not a twin.
	other, _ := store.CreateTarget(&Target{CategoryID: cat, Slug: "a6b", Title: "A6b",
		Host: "example.net", Proto: "tcp", Port: 80, Family: 6, IntervalS: 60, Packets: 5,
		SpacingMs: 500, TimeoutMs: 2000, Public: true, Enabled: true})
	_ = other
	if got, _ := store.FindTwin(mustTarget(t, store, h4)); got != nil && got.ID != h6 {
		t.Error("a different port must not be taken for a twin")
	}
}

func mustTarget(t *testing.T, s *Store, id int64) *Target {
	t.Helper()
	v, err := s.TargetByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A target that states no address family can change family under its own
// history. The back-office needs to say which one its measurements used,
// and above all when they used both.
func TestFamilyOfAddresses(t *testing.T) {
	cases := []struct {
		name   string
		addrs  []string
		family int
		mixed  bool
	}{
		{"only IPv4", []string{"192.0.2.1", "192.0.2.9"}, 4, false},
		{"only IPv6", []string{"2001:db8::1"}, 6, false},
		{"both, which is the defect worth naming",
			[]string{"192.0.2.1", "2001:db8::1"}, 0, true},
		{"both, the other way round",
			[]string{"2001:db8::1", "192.0.2.1"}, 0, true},
		{"nothing measured yet", nil, 0, false},
		{"unparseable entries are ignored", []string{"not-an-address", "192.0.2.1"}, 4, false},
		{"an IPv4-mapped address counts as IPv4", []string{"::ffff:192.0.2.1"}, 4, false},
	}
	for _, c := range cases {
		f, m := familyOfAddresses(c.addrs)
		if f != c.family || m != c.mixed {
			t.Errorf("%s: got family %d mixed %v, want %d %v", c.name, f, m, c.family, c.mixed)
		}
	}
}

// The targets a fresh instance starts with are RIPE Atlas anchors, not
// public resolvers: an anchor exists to be measured, which is consent its
// operator actually gave. The shape of the list is checked here; the names
// themselves were resolved before being written.
func TestDefaultAnchors(t *testing.T) {
	if len(defaultAnchors) < 3 {
		t.Fatalf("a first run needs a few targets, got %d", len(defaultAnchors))
	}
	seenSlug, seenHost, countries := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range defaultAnchors {
		if seenSlug[a.slug] || seenHost[a.host] {
			t.Errorf("duplicate default target: %s / %s", a.slug, a.host)
		}
		seenSlug[a.slug], seenHost[a.host] = true, true
		if !strings.HasSuffix(a.host, ".anchors.atlas.ripe.net") {
			t.Errorf("%s is not an Atlas anchor: a default target must be a host "+
				"whose operator put it there to be measured", a.host)
		}
		if net.ParseIP(strings.TrimSuffix(a.host, ".anchors.atlas.ripe.net")) != nil {
			t.Errorf("%s should be a name, so that a decommissioned anchor fails "+
				"visibly rather than pinging whoever inherits the address", a.host)
		}
		countries[strings.SplitN(a.host, "-", 2)[0]] = true
		if a.title == "" || a.slug == "" {
			t.Errorf("a default target needs a title and a slug: %+v", a)
		}
	}
	// Spread matters: an instance is installed anywhere, and a monitoring
	// tool whose first screen shows one region teaches that the internet is
	// that region.
	if len(countries) < 3 {
		t.Errorf("the default targets span %d countries, which is not a picture "+
			"of the internet", len(countries))
	}
}

// Parameters set on a category apply to every target that leaves the field
// empty, and changing them changes what those targets measure at once —
// dynamic binding, rather than values copied into each target at creation.
func TestCategoryParameterInheritance(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("transit", "Transit", "Transit", true)
	mk := func(slug string, interval int64, packets int) int64 {
		id, err := store.CreateTarget(&Target{CategoryID: cat, Slug: slug, Title: slug,
			Host: "192.0.2.9", Proto: "icmp", Family: 4, IntervalS: interval,
			Packets: packets, SpacingMs: 0, TimeoutMs: 0, Public: true, Enabled: true})
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		return id
	}
	inherits := mk("inherits", 0, 0)
	states := mk("states", 300, 5)

	// Nothing set on the category: the shipped values fill the gaps.
	eff := func(id int64) *Target {
		for _, x := range mustActive(t, store) {
			if x.ID == id {
				return x
			}
		}
		t.Fatalf("target %d not found", id)
		return nil
	}
	if e := eff(inherits); e.IntervalS != 60 || e.Packets != 20 || e.SpacingMs != 500 {
		t.Errorf("shipped values should fill the gaps: %d/%d/%d",
			e.IntervalS, e.Packets, e.SpacingMs)
	}

	// Set on the category: the inheriting target follows, the explicit one
	// does not.
	if err := store.SetCategoryParams(cat, TargetParams{IntervalS: 30, Packets: 10,
		SpacingMs: 200, TimeoutMs: 1000, LossWarn: 5}); err != nil {
		t.Fatal(err)
	}
	if e := eff(inherits); e.IntervalS != 30 || e.Packets != 10 || e.LossWarn != 5 {
		t.Errorf("the category's values should apply: %d/%d/%v",
			e.IntervalS, e.Packets, e.LossWarn)
	}
	if e := eff(states); e.IntervalS != 300 || e.Packets != 5 {
		t.Errorf("a target that states its own values keeps them: %d/%d",
			e.IntervalS, e.Packets)
	}

	// Dynamic binding: change the category, the inheriting target changes
	// with it, and nothing was written to the target itself.
	if err := store.SetCategoryParams(cat, TargetParams{IntervalS: 120, Packets: 10,
		SpacingMs: 200, TimeoutMs: 1000}); err != nil {
		t.Fatal(err)
	}
	if e := eff(inherits); e.IntervalS != 120 {
		t.Errorf("the change should follow: interval %d", e.IntervalS)
	}
	var stored int64
	store.cfg.QueryRow(`SELECT interval_s FROM targets WHERE id=?`, inherits).Scan(&stored)
	if stored != 0 {
		t.Errorf("nothing should be copied into the target, it holds %d", stored)
	}

	// The counts tell the operator how far an edit reaches, before he saves.
	counts := store.InheritCounts(cat)
	if counts["interval_s"] != 1 || counts["spacing_ms"] != 2 {
		t.Errorf("inherit counts: %+v", counts)
	}
}

// Every target of an instance upgrading to this version carries explicit
// values, so a category parameter would change nothing without a way to put
// them back to inheriting.
func TestResetToInherit(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("c", "C", "C", true)
	for _, slug := range []string{"a", "b"} {
		if _, err := store.CreateTarget(&Target{CategoryID: cat, Slug: slug, Title: slug,
			Host: "192.0.2.9", Proto: "icmp", Family: 4, IntervalS: 600, Packets: 30,
			SpacingMs: 500, TimeoutMs: 2000, Public: true, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	store.SetCategoryParams(cat, TargetParams{IntervalS: 60, Packets: 10,
		SpacingMs: 200, TimeoutMs: 1000})
	// Explicit values win, so the category changed nothing yet.
	if e := mustActive(t, store)[0]; e.IntervalS != 600 {
		t.Fatalf("explicit value should still win: %d", e.IntervalS)
	}
	n, err := store.ResetToInherit(cat, []string{"interval_s", "packets"})
	if err != nil || n != 2 {
		t.Fatalf("two targets expected, got %d: %v", n, err)
	}
	for _, e := range mustActive(t, store) {
		if e.IntervalS != 60 || e.Packets != 10 {
			t.Errorf("%s should now inherit: %d/%d", e.Slug, e.IntervalS, e.Packets)
		}
		// Only the named fields were cleared.
		var sp int
		store.cfg.QueryRow(`SELECT spacing_ms FROM targets WHERE id=?`, e.ID).Scan(&sp)
		if sp != 500 {
			t.Errorf("spacing was not named and must be untouched, got %d", sp)
		}
	}
	if _, err := store.ResetToInherit(cat, []string{"host"}); err == nil {
		t.Error("the host is not something a category lends")
	}
	if _, err := store.ResetToInherit(cat, nil); err == nil {
		t.Error("resetting nothing must be refused rather than silently doing nothing")
	}
}

// A category must not be able to push a target into a state the target
// itself would have been refused.
func TestCategoryCannotBreakItsTargets(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cat, _ := store.CreateCategory("c", "C", "C", true)
	// 30 packets spaced by 500 ms plus a 2 s timeout needs a long interval.
	if _, err := store.CreateTarget(&Target{CategoryID: cat, Slug: "slow", Title: "Slow",
		Host: "192.0.2.9", Proto: "icmp", Family: 4, Packets: 30, SpacingMs: 500,
		TimeoutMs: 2000, IntervalS: 0, Public: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// A 10-second interval lent by the category would not fit that burst.
	if err := store.SetCategoryParams(cat, TargetParams{IntervalS: 10}); err != nil {
		t.Fatalf("storing is allowed, the check is separate: %v", err)
	}
	bad, err := store.CategoryWouldBreak(cat)
	if err != nil {
		t.Fatal(err)
	}
	if bad == "" {
		t.Error("a category value that breaks a target must be reported")
	}
	if !strings.Contains(bad, "Slow") {
		t.Errorf("the report must name the target: %q", bad)
	}
	// Values a target would be refused are refused on the category too.
	if err := checkParams(TargetParams{Packets: 500}); err == nil {
		t.Error("a category cannot lend 500 packets")
	}
	if err := checkParams(TargetParams{LossCrit: 1, LossWarn: 9}); err == nil {
		t.Error("a critical threshold below the warning one is incoherent anywhere")
	}
	if err := checkParams(TargetParams{}); err != nil {
		t.Errorf("a category lending nothing is valid: %v", err)
	}
}

func mustActive(t *testing.T, s *Store) []*Target {
	t.Helper()
	ts, err := s.ActiveTargets()
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// A pinned address is shown to visitors as the network it belongs to, not as
// the machine. The notice exists to say the figures describe one machine,
// which does not require naming it to everyone who opens the page.
func TestMaskIP(t *testing.T) {
	cases := map[string]string{
		"142.251.153.4":            "142.251.XXX.XXX",
		"9.9.9.9":                  "9.9.XXX.XXX",
		"::ffff:192.0.2.1":         "192.0.XXX.XXX",
		"2a00:1450:4007:80f::200e": "2a00:1450:XXXX:XXXX::",
		"2001:db8::1":              "2001:db8:XXXX:XXXX::",
		"not-an-address":           "",
		"":                         "",
	}
	for in, want := range cases {
		if got := maskIP(in); got != want {
			t.Errorf("maskIP(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]int{
		"142.251.153.4": 4, "2001:db8::1": 6, "::ffff:192.0.2.1": 4, "bogus": 0,
	} {
		if got := familyOf(in); got != want {
			t.Errorf("familyOf(%q) = %d, want %d", in, got, want)
		}
	}
}

// The about page states where the packets leave from. The address itself is
// never part of it: the reverse name and the network situate the probe, the
// same rule the targets follow.
func TestInstanceInfoPublishesNoAddress(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := &API{store: store, asn: NewASNService(store, nil, "")}
	api.refreshInstanceNetwork()
	v := api.instanceInfo()

	if v.Network != "" && !strings.Contains(v.Network, "XXX") {
		t.Errorf("the network must be masked, got %q", v.Network)
	}
	if v.Network != "" && net.ParseIP(v.Network) != nil {
		t.Errorf("a whole address reached the public payload: %q", v.Network)
	}
	// The machine it runs on is described, which is what makes the figures
	// readable: a burst is not the same work on two cores and on thirty-two.
	if v.CPUCores < 1 {
		t.Error("the processor count should be reported")
	}
	if v.Platform == "" || v.Go == "" {
		t.Errorf("platform and runtime should be reported: %+v", v)
	}
	// The two places a reader checks an AS number for himself.
	l := asnLinks("AS2484")
	if l["peeringdb"] != "https://www.peeringdb.com/asn/2484" {
		t.Errorf("PeeringDB link: %q", l["peeringdb"])
	}
	if l["ripe"] != "https://stat.ripe.net/app/launchpad/AS2484" {
		t.Errorf("RIPE link: %q", l["ripe"])
	}
	if asnLinks("not an AS") != nil || asnLinks("") != nil {
		t.Error("a link is only built for a real AS number")
	}
}
