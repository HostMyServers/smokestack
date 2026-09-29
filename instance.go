package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ce que l'instance dit d'elle-meme sur sa page « a propos ». Une mesure
// sans origine declaree ne vaut rien pour celui qui la lit : savoir d'ou
// partent les paquets, et dans quel reseau, est ce qui permet a un tiers
// d'en faire quelque chose.
//
// L'adresse elle-meme n'y figure pas. Le nom inverse et le reseau suffisent
// a situer la sonde, la meme regle que pour les cibles : on publie de quel
// reseau on parle, pas quelle machine.

type InstanceInfo struct {
	Version   string `json:"version"`
	BuildDate string `json:"build_date,omitempty"`
	Platform  string `json:"platform"`
	Go        string `json:"go"`
	UptimeS   int64  `json:"uptime_s"`

	CPUCores int    `json:"cpu_cores"`
	CPUModel string `json:"cpu_model,omitempty"`
	RAMBytes int64  `json:"ram_bytes,omitempty"`

	// Reseau de la sonde. Network est l'adresse masquee : le reseau, pas la
	// machine. Reverse est le nom inverse quand il existe, qui situe la
	// sonde sans rien reveler de plus que ce que le DNS publie deja.
	Reverse   string `json:"reverse,omitempty"`
	Network   string `json:"network,omitempty"`
	Family    int    `json:"family,omitempty"`
	ASN       string `json:"asn,omitempty"`
	ASName    string `json:"as_name,omitempty"`
	NetName   string `json:"net_name,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
	Behind    bool   `json:"behind_nat,omitempty"`
	FetchedAt int64  `json:"fetched_at,omitempty"`
}

var startedAt = time.Now()

// sourceIP is the address a packet leaves with, which is what a target sees
// and therefore the one worth describing. A UDP "connection" performs the
// route lookup without sending anything.
func sourceIP(network, probe string) net.IP {
	c, err := net.Dial(network, probe)
	if err != nil {
		return nil
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP
	}
	return nil
}

// cpuModel reads the processor name where the system publishes it. Absent
// on anything but Linux, and absent on some Linux too, which is why every
// field of this page degrades to nothing rather than to a guess.
func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name", "Model", "Hardware", "cpu model":
			return oneLine(strings.TrimSpace(v), 80)
		}
	}
	return ""
}

func ramBytes() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(k) != "MemTotal" {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

type instanceCache struct {
	mu   sync.Mutex
	val  InstanceInfo
	when time.Time
}

var instCache instanceCache

// instanceInfo answers from cache and refreshes in the background. The
// reverse lookup and the AS attribution both cross the network, and a
// visitor must never wait on either.
func (a *API) instanceInfo() InstanceInfo {
	instCache.mu.Lock()
	v, when := instCache.val, instCache.when
	instCache.mu.Unlock()

	v.Version, v.BuildDate = Version, BuildDate
	v.Platform = runtime.GOOS + "-" + runtime.GOARCH
	v.Go = runtime.Version()
	v.UptimeS = int64(time.Since(startedAt).Seconds())
	v.CPUCores = runtime.NumCPU()
	if v.CPUModel == "" {
		v.CPUModel = cpuModel()
	}
	if v.RAMBytes == 0 {
		v.RAMBytes = ramBytes()
	}
	if time.Since(when) > 30*time.Minute {
		go a.refreshInstanceNetwork()
		if when.IsZero() {
			v.Pending = true
		}
	}
	return v
}

func (a *API) refreshInstanceNetwork() {
	var out InstanceInfo
	// IPv4 first, IPv6 when the host has no IPv4 route: the page describes
	// one probe, and the family it actually leaves with.
	ip, fam := sourceIP("udp4", "192.0.2.1:9"), 4
	if ip == nil {
		ip, fam = sourceIP("udp6", "[2001:db8::1]:9"), 6
	}
	if ip == nil {
		return
	}
	out.Family = fam
	if !isPublicIP(ip) {
		// Behind NAT the local address says nothing useful and must not be
		// mistaken for the address targets see.
		out.Behind = true
	} else {
		out.Network = maskIP(ip.String())
		if names, err := net.LookupAddr(ip.String()); err == nil && len(names) > 0 {
			out.Reverse = strings.TrimSuffix(oneLine(names[0], 120), ".")
		}
		if asn, known := a.asn.ASNOfIP(ip.String()); known && asn != "" {
			out.ASN = asn
			if info, _ := a.asn.Cached(strings.TrimPrefix(asn, "AS")); info != nil {
				out.ASName = info.Holder
				if info.PeeringDB != nil && info.PeeringDB.Name != "" {
					out.NetName = info.PeeringDB.Name
				}
			} else {
				go a.asn.Refresh(strings.TrimPrefix(asn, "AS"))
			}
		} else {
			a.asn.RefreshIPASN(ip.String())
		}
	}
	out.FetchedAt = time.Now().Unix()

	instCache.mu.Lock()
	out.CPUModel, out.RAMBytes = instCache.val.CPUModel, instCache.val.RAMBytes
	instCache.val, instCache.when = out, time.Now()
	instCache.mu.Unlock()
}

func (a *API) instanceGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, a.instanceInfo())
}

// asnLinks gives the two places a reader checks an AS number for himself.
// Published rather than described: an instance that says which network it
// measures from should make that claim verifiable in one click.
func asnLinks(asn string) map[string]string {
	n := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(asn)), "AS")
	if n == "" {
		return nil
	}
	if _, err := strconv.Atoi(n); err != nil {
		return nil
	}
	return map[string]string{
		"peeringdb": "https://www.peeringdb.com/asn/" + n,
		"ripe":      "https://stat.ripe.net/app/launchpad/AS" + n,
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.0f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n > 0:
		return fmt.Sprintf("%d B", n)
	}
	return ""
}
