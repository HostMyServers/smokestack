package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// An internet exchange is where two networks hand traffic over to each
// other, and it is the one hop of a traceroute that the usual lookup cannot
// name: a peering LAN is deliberately kept out of the global routing table,
// so the Team Cymru lookup that names every other hop answers nothing for a
// router sitting on one. The hop an operator cares about most was the only
// blank line of the table.
//
// PeeringDB publishes both halves of what is missing: which prefixes belong
// to which exchange, and which member holds each address on them.
//
// The prefix table is small — about 2 600 entries — and moves in weeks: it
// is fetched once a week, kept in memory for the lookups and in config.db so
// that a restart does not wait on PeeringDB. Member addresses are asked for
// one at a time, in the background, and cached like any other address. A
// page never waits for either.

const (
	ixPrefixesKey = "ixp:prefixes"
	ixPrefixesTTL = 7 * 24 * time.Hour
	ixMemberKey   = "ixasn:" // ixasn:<addr> -> "AS2200", or "-" if undeclared
)

type ixNet struct {
	net *net.IPNet
	ix  string
}

// ixTable indexes the peering LANs for lookup: IPv4 by first octet, IPv6 as
// one list — there are a few hundred of them, and a hop is looked up once
// per request whatever the number of traceroutes shown.
type ixTable struct {
	v4 map[byte][]ixNet
	v6 []ixNet
	at int64
}

// ixStored is the table as config.db holds it: prefix to exchange name.
type ixStored struct {
	At   int64             `json:"at"`
	Nets map[string]string `json:"nets"`
}

func buildIXTable(st ixStored) *ixTable {
	t := &ixTable{v4: map[byte][]ixNet{}, at: st.At}
	for p, name := range st.Nets {
		_, n, err := net.ParseCIDR(p)
		if err != nil || name == "" {
			continue
		}
		e := ixNet{net: n, ix: name}
		if v4 := n.IP.To4(); v4 != nil {
			t.v4[v4[0]] = append(t.v4[v4[0]], e)
		} else {
			t.v6 = append(t.v6, e)
		}
	}
	return t
}

// loadIXTable reads the table config.db holds, if it holds one.
func loadIXTable(store *Store) *ixTable {
	if store == nil {
		return nil
	}
	raw := store.Setting(ixPrefixesKey, "")
	if raw == "" {
		return nil
	}
	var st ixStored
	if json.Unmarshal([]byte(raw), &st) != nil {
		return nil
	}
	return buildIXTable(st)
}

// A table that was just fetched is the table: setting it also marks the
// lazy load done, so it cannot come back over it with the older one.
func (s *ASNService) setIXTable(t *ixTable) {
	s.ixMu.Lock()
	s.ix, s.ixLoaded = t, true
	s.ixMu.Unlock()
}

// ixNets returns the table, read from config.db the first time it is needed.
// An instance that has never reached PeeringDB simply has none.
func (s *ASNService) ixNets() *ixTable {
	s.ixMu.RLock()
	t, loaded := s.ix, s.ixLoaded
	s.ixMu.RUnlock()
	if loaded {
		return t
	}
	s.ixMu.Lock()
	defer s.ixMu.Unlock()
	if !s.ixLoaded {
		s.ix, s.ixLoaded = loadIXTable(s.store), true
	}
	return s.ix
}

// IXAt names the exchange whose peering LAN holds an address, if any.
func (s *ASNService) IXAt(ip net.IP) string {
	t := s.ixNets()
	if ip == nil || t == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		for _, n := range t.v4[v4[0]] {
			if n.net.Contains(ip) {
				return n.ix
			}
		}
		return ""
	}
	for _, n := range t.v6 {
		if n.net.Contains(ip) {
			return n.ix
		}
	}
	return ""
}

func (s *ASNService) ixStale() bool {
	t := s.ixNets()
	return t == nil || time.Since(time.Unix(t.at, 0)) > ixPrefixesTTL
}

// refreshIXPrefixes rebuilds the map of peering LANs from PeeringDB: the
// exchanges, the LANs they operate, and the prefixes on those LANs. Three
// calls asking for the handful of fields that matter, about 350 KB in all.
func (s *ASNService) refreshIXPrefixes() error {
	if s.store == nil {
		return errors.New("no store")
	}
	var ixs struct {
		Data []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Long string `json:"name_long"`
		} `json:"data"`
	}
	if err := s.getJSON(s.pdbBase+"ix?fields=id,name,name_long", true, &ixs); err != nil {
		return err
	}
	var lans struct {
		Data []struct {
			ID   int `json:"id"`
			IXID int `json:"ix_id"`
		} `json:"data"`
	}
	if err := s.getJSON(s.pdbBase+"ixlan?fields=id,ix_id", true, &lans); err != nil {
		return err
	}
	var pfx struct {
		Data []struct {
			LanID  int    `json:"ixlan_id"`
			Prefix string `json:"prefix"`
		} `json:"data"`
	}
	if err := s.getJSON(s.pdbBase+"ixpfx?fields=ixlan_id,prefix", true, &pfx); err != nil {
		return err
	}

	name := map[int]string{}
	for _, x := range ixs.Data {
		n := strings.TrimSpace(x.Name)
		if n == "" {
			n = strings.TrimSpace(x.Long)
		}
		if n != "" {
			name[x.ID] = oneLine(n, 48)
		}
	}
	ofLan := map[int]int{}
	for _, l := range lans.Data {
		ofLan[l.ID] = l.IXID
	}
	nets := map[string]string{}
	for _, p := range pfx.Data {
		n := name[ofLan[p.LanID]]
		if n == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(p.Prefix); err != nil {
			continue
		}
		nets[p.Prefix] = n
	}
	// A truncated answer must not replace a table that works.
	if len(nets) < 500 {
		return fmt.Errorf("PeeringDB returned %d peering LANs", len(nets))
	}
	st := ixStored{At: time.Now().Unix(), Nets: nets}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	s.setIXTable(buildIXTable(st))
	return s.store.SetSetting(ixPrefixesKey, string(b))
}

// ------------------------------------------------------- member addresses

// ixMember returns the AS declaring an address on a peering LAN, and whether
// the question has already been asked. "-" is an answer: the exchange knows
// the prefix, no member claims that address.
func (s *ASNService) ixMember(addr string) (string, bool) {
	if s.store == nil {
		return "", false
	}
	switch v := s.store.Setting(ixMemberKey+addr, ""); v {
	case "":
		return "", false
	case "-":
		return "", true
	default:
		return v, true
	}
}

// refreshIXMember asks PeeringDB which member holds an address on a peering
// LAN. This is what the member declares, not what the routing table says —
// the routing table has nothing to say about these prefixes.
func (s *ASNService) refreshIXMember(addr string) {
	ip := net.ParseIP(addr)
	if ip == nil || s.store == nil {
		return
	}
	field := "ipaddr4"
	if ip.To4() == nil {
		field = "ipaddr6"
	}
	var out struct {
		Data []struct {
			ASN int `json:"asn"`
		} `json:"data"`
	}
	q := s.pdbBase + "netixlan?fields=asn&" + field + "=" + url.QueryEscape(ip.String())
	if err := s.getJSON(q, true, &out); err != nil {
		return // a call that failed is not an answer: it is asked again later
	}
	v := "-"
	if len(out.Data) > 0 && out.Data[0].ASN > 0 {
		v = "AS" + strconv.Itoa(out.Data[0].ASN)
	}
	s.store.SetSetting(ixMemberKey+addr, v)
}

// ---------------------------------------------------------- resolve queue

// queueAddr asks for an address to be named in the background. The page that
// wanted it gets it on the next view; none ever waits for a lookup.
func (s *ASNService) queueAddr(addr string) {
	s.mu.Lock()
	if s.asked == nil {
		s.asked = map[string]bool{}
	}
	if s.asked[addr] || len(s.asked) > 512 {
		s.mu.Unlock()
		return
	}
	s.asked[addr] = true
	s.mu.Unlock()

	s.jobsOnce.Do(func() {
		s.jobs = make(chan string, 256)
		go s.resolveLoop()
	})
	select {
	case s.jobs <- addr:
	default:
		s.forget(addr) // the queue is full: the next view asks again
	}
}

func (s *ASNService) forget(addr string) {
	s.mu.Lock()
	delete(s.asked, addr)
	s.mu.Unlock()
}

func (s *ASNService) resolveLoop() {
	for addr := range s.jobs {
		s.resolveAddr(addr)
		// Courtesy towards Team Cymru and PeeringDB: a target whose path
		// changed brings a dozen new addresses at once, not a flood.
		time.Sleep(250 * time.Millisecond)
	}
}

// resolveAddr names an address: the global routing table first, and when
// nothing announces it and it sits on a peering LAN, the exchange member
// that holds it.
func (s *ASNService) resolveAddr(addr string) {
	defer s.forget(addr)
	as, known := s.ASNOfIP(addr)
	if !known {
		s.RefreshIPASN(addr)
		as, _ = s.ASNOfIP(addr)
	}
	if as != "" || s.IXAt(net.ParseIP(addr)) == "" {
		return
	}
	if _, asked := s.ixMember(addr); !asked {
		s.refreshIXMember(addr)
	}
}

// ------------------------------------------------------------------- hops

type hopName struct{ asn, ix string }

// HopFiller returns a function that names the hops a probe could not: the
// autonomous system from the cache, and the exchange when the address sits
// on a peering LAN. The caches alone answer it, and what is missing is
// queued for later — so a traceroute that was recorded before an address
// could be resolved is drawn complete once it has been.
//
// Every traceroute of a target repeats the same addresses, so the lookups
// are kept for the request that asked.
func (s *ASNService) HopFiller() func([]Hop) {
	seen := map[string]hopName{}
	return func(hops []Hop) {
		for i := range hops {
			h := &hops[i]
			if h.Addr == "" {
				continue
			}
			n, ok := seen[h.Addr]
			if !ok {
				n = s.nameAddr(h.Addr)
				seen[h.Addr] = n
			}
			if h.ASN == "" {
				h.ASN = n.asn
			}
			h.IX = n.ix
		}
	}
}

func (s *ASNService) nameAddr(addr string) hopName {
	ip := net.ParseIP(addr)
	if ip == nil || !isPublicIP(ip) || s.store == nil {
		return hopName{}
	}
	out := hopName{ix: s.IXAt(ip)}
	as, known := s.ASNOfIP(addr)
	out.asn = as
	if out.asn != "" {
		return out
	}
	if out.ix == "" {
		// Not a peering LAN: either the lookup never ran, or it ran and
		// nothing announces the address. Only the first is worth asking.
		if !known {
			s.queueAddr(addr)
		}
		return out
	}
	if as, asked := s.ixMember(addr); asked {
		out.asn = as
	} else {
		s.queueAddr(addr)
	}
	return out
}
