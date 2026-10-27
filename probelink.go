package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Sonde isolee dans son propre processus (probe.mode = "external").
//
//	smokestack          service web + ecrivain, SANS socket ICMP brut
//	smokestack probe    sonde seule, CAP_NET_RAW, prioritaire sur le CPU
//
// Les deux dialoguent par un socket Unix (0660, utilisateur du service) :
//
//	GET  /probe/v1/targets       cibles actives + version du service
//	POST /probe/v1/measurements  lot de mesures -> file de l'ecrivain
//
// La sonde ne partage ni memoire, ni ramasse-miettes, ni ordonnanceur avec
// l'interface web : une page lourde ne peut plus retarder une mesure. Si
// le service redemarre, la sonde conserve ses mesures en memoire et les
// renvoie ensuite. Apres une mise a jour, elle se relance d'elle-meme sur
// la nouvelle version.

const (
	probeBufferMax  = 200000 // ~ 1 h de mesures pour 1 000 cibles a 30 s
	probeFlushEvery = time.Second
	probeFlushMax   = 5000
)

type wireMeasure struct {
	Measurement
	Host string `json:"host"`
}

type probeTargetsResp struct {
	Version       string    `json:"version"`
	ProbeID       int64     `json:"probe_id"`
	Targets       []*Target `json:"targets"`
	TraceRequests []int64   `json:"trace_requests"`
	CheckRequests []int64   `json:"check_requests"`
}

func probeSocketPath(cfg Config) string {
	if cfg.Probe.Socket != "" {
		return cfg.Probe.Socket
	}
	return filepath.Join(cfg.DataDir, "probe.sock")
}

// ---------------------------------------------------------------- service

// probeEndpoints registers the three endpoints the probe protocol is made of.
//
// The Unix socket and the public listener serve exactly these, because a
// remote probe and a local one have to be measuring the same thing and
// reporting it the same way. They differ in one respect: who is calling. The
// socket is reachable only by a process on the host and answers for the local
// probe without asking; the listener is reachable from anywhere and resolves
// the probe from its token, refusing everything it cannot resolve.
//
// Keeping one protocol is also what keeps /api/v1/ingest honest. That
// endpoint exists for anything speaking to us from outside and carries no
// host field and no way to learn a target list, so a probe built on it would
// have been a second, poorer dialect of this one.
func probeEndpoints(mux *http.ServeMux, cache *TargetCache, writer *Writer,
	who func(*http.Request) (int64, bool)) {
	mux.HandleFunc("GET /probe/v1/targets", func(w http.ResponseWriter, r *http.Request) {
		probeID, ok := who(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "a probe token is required")
			return
		}
		// This answer carries every active target, private ones included,
		// because a probe has to measure them. Nothing between a probe and
		// the service may hold it, and over the listener that is not
		// theoretical: a token is a read credential for the whole list.
		cachePrivate(w)
		// The traceroute and check queues are global and popped by whoever
		// asks first. With a single probe that is the probe; with several it
		// means an anomaly on one probe's target can be traced from another.
		// Routing these per probe belongs with the per-target primary probe,
		// which is a separate decision, so this stays as it is rather than
		// growing a half-rule now.
		writeJSON(w, probeTargetsResp{Version: Version, ProbeID: probeID, Targets: cache.Targets(),
			TraceRequests: traceRequests.Pop(), CheckRequests: checkRequests.Pop()})
	})
	mux.HandleFunc("POST /probe/v1/traceroutes", func(w http.ResponseWriter, r *http.Request) {
		probeID, ok := who(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "a probe token is required")
			return
		}
		var list []*Traceroute
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&list); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		for _, tr := range list {
			// Forced from the caller's identity, never read from the body:
			// otherwise a probe could file its traceroutes under another.
			tr.ProbeID = probeID
			writer.SubmitTrace(tr)
		}
		writeJSON(w, map[string]int{"accepted": len(list)})
	})
	mux.HandleFunc("POST /probe/v1/measurements", func(w http.ResponseWriter, r *http.Request) {
		probeID, ok := who(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "a probe token is required")
			return
		}
		var batch []wireMeasure
		// The same ceilings as the HTTP ingestion endpoint — see
		// maxIngestBytes. One payload shape, one limit.
		if err := json.NewDecoder(io.LimitReader(r.Body, maxIngestBytes)).Decode(&batch); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if len(batch) > maxIngestBatch {
			writeErr(w, http.StatusRequestEntityTooLarge, "too many measurements in one batch")
			return
		}
		for _, b := range batch {
			b.Measurement.ProbeID = probeID
			writer.Submit(b.Measurement, b.Host)
		}
		writeJSON(w, map[string]int{"accepted": len(batch)})
	})
}

// ProbeRoutes exposes the probe protocol to probes that run on another
// machine. It is registered only when the operator has enrolled at least the
// possibility of one: with no token issued anywhere, every request here is
// refused, which is the same thing as the endpoints not existing.
func ProbeRoutes(mux *http.ServeMux, store *Store, cache *TargetCache, writer *Writer) {
	probeEndpoints(mux, cache, writer, func(r *http.Request) (int64, bool) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			return 0, false
		}
		id, ok := store.ProbeByToken(strings.TrimPrefix(auth, "Bearer "))
		if ok {
			store.TouchProbe(id)
		}
		return id, ok
	})
}

func ServeProbeSocket(path string, cache *TargetCache, writer *Writer, probeID int64, stop <-chan struct{}) error {
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	os.Chmod(path, 0o660)
	mux := http.NewServeMux()
	// Reaching this socket already means being a process on the host under
	// the service's group, which is the whole authentication it has ever had.
	probeEndpoints(mux, cache, writer, func(*http.Request) (int64, bool) { return probeID, true })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-stop
		srv.Close()
		os.Remove(path)
	}()
	log.Printf("waiting for the external probe on %s", path)
	go srv.Serve(l)
	return nil
}

// ------------------------------------------------------------------ sonde

type remoteLink struct {
	client *http.Client
	// Where the service is and how to prove who we are. Over the Unix socket
	// the host in the URL is a placeholder the dialer ignores and there is no
	// token, because reaching the socket is the proof. Over the network both
	// carry their real meaning.
	base  string
	token string

	mu      sync.RWMutex
	targets []*Target
	version string
	probeID int64

	bmu     sync.Mutex
	buf     []wireMeasure
	dropped int64
	traces  []*Traceroute
	treqs   []int64
	creqs   []int64
}

func newRemoteLink(sock string) *remoteLink {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns: 2,
	}
	return &remoteLink{base: "http://probe",
		client: &http.Client{Transport: tr, Timeout: 15 * time.Second}}
}

// newRemoteLinkHTTP talks to a service on another machine. Everything past
// this constructor is the same code as the socket, which is the point: the
// two differ in their transport and in nothing else.
//
// The scheme is required to be https. A probe token travels on every request
// and the target list comes back on the same connection, so plain http would
// hand both to anyone on the path; the probe refuses rather than warning,
// because a warning in a log nobody reads is not a choice the operator made.
func newRemoteLinkHTTP(base, token string) (*remoteLink, error) {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if !strings.HasPrefix(base, "https://") {
		return nil, fmt.Errorf("probe.server must be an https:// URL, got %q", base)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("probe.token is required when probe.server is set")
	}
	tr := &http.Transport{MaxIdleConns: 2, IdleConnTimeout: 90 * time.Second}
	return &remoteLink{base: base, token: strings.TrimSpace(token),
		client: &http.Client{Transport: tr, Timeout: 15 * time.Second}}, nil
}

// do is the one place a request is built, so the token cannot be forgotten on
// one of the three calls.
func (l *remoteLink) do(method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, l.base+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if l.token != "" {
		req.Header.Set("Authorization", "Bearer "+l.token)
	}
	return l.client.Do(req)
}

func (l *remoteLink) Targets() []*Target {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.targets
}

// Submit ne bloque jamais : la mesure part dans le tampon memoire.
func (l *remoteLink) Submit(m Measurement, host string) {
	l.bmu.Lock()
	defer l.bmu.Unlock()
	if len(l.buf) >= probeBufferMax {
		l.buf = l.buf[1:]
		l.dropped++
	}
	l.buf = append(l.buf, wireMeasure{Measurement: m, Host: host})
}

func (l *remoteLink) SubmitTrace(tr *Traceroute) {
	l.bmu.Lock()
	defer l.bmu.Unlock()
	if len(l.traces) < 1000 {
		l.traces = append(l.traces, tr)
	}
}

func (l *remoteLink) TraceRequests() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.treqs
	l.treqs = nil
	return out
}

func (l *remoteLink) CheckRequests() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.creqs
	l.creqs = nil
	return out
}

func (l *remoteLink) fetchTargets() error {
	resp, err := l.do("GET", "/probe/v1/targets", "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var tr probeTargetsResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return err
	}
	l.mu.Lock()
	l.targets, l.version, l.probeID = tr.Targets, tr.Version, tr.ProbeID
	l.treqs = append(l.treqs, tr.TraceRequests...)
	l.creqs = append(l.creqs, tr.CheckRequests...)
	l.mu.Unlock()
	return nil
}

func (l *remoteLink) flush() error {
	l.bmu.Lock()
	traces := l.traces
	l.traces = nil
	l.bmu.Unlock()
	if len(traces) > 0 {
		body, _ := json.Marshal(traces)
		resp, err := l.do("POST", "/probe/v1/traceroutes", "application/json", bytes.NewReader(body))
		if err != nil || resp.StatusCode != 200 {
			l.bmu.Lock()
			l.traces = append(traces, l.traces...)
			l.bmu.Unlock()
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	l.bmu.Lock()
	n := len(l.buf)
	if n > probeFlushMax {
		n = probeFlushMax
	}
	batch := append([]wireMeasure(nil), l.buf[:n]...)
	l.bmu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	body, _ := json.Marshal(batch)
	resp, err := l.do("POST", "/probe/v1/measurements", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	l.bmu.Lock()
	l.buf = l.buf[len(batch):]
	l.bmu.Unlock()
	return nil
}

// currentBinary renvoie le binaire actif (<racine>/current/smokestack)
// quand la sonde tourne depuis une installation geree.
func currentBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	releases := filepath.Dir(filepath.Dir(exe))
	if filepath.Base(releases) == "releases" {
		cur := filepath.Join(filepath.Dir(releases), "current", "smokestack")
		if real, err := filepath.EvalSymlinks(cur); err == nil && real != exe {
			return cur
		}
	}
	return ""
}

func runProbe(args []string) error {
	fsn := flag.NewFlagSet("probe", flag.ExitOnError)
	cfgPath := cliConfig(fsn)
	fsn.Parse(args)
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	// Remote when the operator named a service, local otherwise. The local
	// path is untouched: an installation that has always used the socket
	// never sets probe.server and never sees any of this.
	remote := strings.TrimSpace(cfg.Probe.Server) != ""
	var link *remoteLink
	if remote {
		link, err = newRemoteLinkHTTP(cfg.Probe.Server, cfg.Probe.Token)
		if err != nil {
			return err
		}
		log.Printf("smokestack probe %s, service at %s", Version, link.base)
	} else {
		sock := probeSocketPath(cfg)
		link = newRemoteLink(sock)
		log.Printf("smokestack probe %s, service on %s", Version, sock)
	}

	for i := 0; ; i++ {
		if err := link.fetchTargets(); err == nil {
			break
		} else if i%10 == 0 {
			log.Printf("waiting for the service: %v", err)
		}
		time.Sleep(time.Second)
	}
	prober, err := NewProber(link, link, link.probeID, cfg.Probe.Traceroute)
	if err != nil {
		return fmt.Errorf("ICMP socket: %w (the probe needs CAP_NET_RAW)", err)
	}
	defer prober.Close()

	stop := make(chan struct{})
	go prober.Schedule(stop)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	refresh := time.NewTicker(5 * time.Second)
	flush := time.NewTicker(probeFlushEvery)
	defer refresh.Stop()
	defer flush.Stop()
	var lastWarn time.Time
	for {
		select {
		case <-sig:
			close(stop)
			// Laisse les passes en cours se terminer, puis vide le tampon.
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				if link.flush() != nil {
					break
				}
				link.bmu.Lock()
				left := len(link.buf)
				link.bmu.Unlock()
				if left == 0 {
					break
				}
			}
			log.Printf("probe stopped")
			return nil
		case <-refresh.C:
			if err := link.fetchTargets(); err != nil {
				continue
			}
			link.mu.RLock()
			v := link.version
			link.mu.RUnlock()
			// Le service a change de version (mise a jour ou retour
			// arriere) : on se relance sur le binaire actif.
			//
			// Only when the two share a machine. A remote probe has its own
			// release tree on its own update cadence, so chasing the
			// service's version would restart it into the same binary again
			// and again, forever, and the operator would see a probe that
			// never settles rather than one that is simply a version behind.
			if !remote && v != "" && v != Version {
				if bin := currentBinary(); bin != "" {
					link.flush()
					log.Printf("service now runs version %s, restarting the probe", v)
					prober.Close()
					if err := syscall.Exec(bin, os.Args, os.Environ()); err != nil {
						log.Printf("restart failed: %v", err)
					}
				}
			}
		case <-flush.C:
			if err := link.flush(); err != nil && time.Since(lastWarn) > time.Minute {
				link.bmu.Lock()
				n, d := len(link.buf), link.dropped
				link.bmu.Unlock()
				log.Printf("service unreachable (%v): %d measurements pending, %d dropped", err, n, d)
				lastWarn = time.Now()
			}
		}
	}
}
