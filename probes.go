package main

// Remote probe enrolment.
//
// A probe running on another machine reaches the instance over HTTP and
// authenticates with a token. The verification side of that already existed
// in ingest(): it reads probes.token_hash, compares in constant time and
// refuses an empty hash. What was missing was any way to issue a token, so
// the remote mode could not be used at all — ProbeID() inserts a probe but
// never writes token_hash, and nothing else ever did.
//
// Only the hash is stored, so a token is shown once and cannot be recovered
// afterwards. That is the same contract as a share link, and for the same
// reason: a stored credential is a credential that leaks with the backup.

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// A probe as the back-office sees it. Token is filled on the one response
// that issues it and is empty everywhere else; token_hash never leaves the
// database at all, under any field name.
type Probe struct {
	ID         int64  `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Location   string `json:"location"`
	Local      bool   `json:"local"`
	Enabled    bool   `json:"enabled"`
	LastSeenAt int64  `json:"last_seen_at"`
	HasToken   bool   `json:"has_token"`
	Token      string `json:"token,omitempty"`

	// Where it measures from. SeenAddress is observed when the probe
	// connects; Address is what the operator stated and wins when set.
	// Address is the repair for the two cases observation gets wrong: a probe
	// behind NAT, and an instance behind a reverse proxy, where what arrives
	// is the proxy rather than the probe.
	Address     string `json:"address"`
	SeenAddress string `json:"seen_address"`
	// Derived, never stored: the address actually used, and the AS it falls
	// in. The AS is resolved from the lookup service's cache at read time, so
	// it follows a renumbering without a column to keep in step.
	From string `json:"from,omitempty"`
	ASN  string `json:"asn,omitempty"`
}

// EffectiveAddress is the stated address when there is one, the observed
// address otherwise. Everything that needs to know where a probe measures
// from asks this rather than picking a column, so the precedence is written
// once.
func (p Probe) EffectiveAddress() string {
	if a := strings.TrimSpace(p.Address); a != "" {
		return a
	}
	return strings.TrimSpace(p.SeenAddress)
}

// The slug identifies the probe in the ingest body and will end up in URLs
// and in the interface. Constraining it here keeps every later use plain,
// rather than leaving each one to guess what a slug can contain.
const probeSlugMax = 39

func validProbeSlug(s string) error {
	if len(s) < 2 || len(s) > probeSlugMax {
		return fmt.Errorf("the slug must be between 2 and %d characters", probeSlugMax)
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(s)-1:
		default:
			return fmt.Errorf("the slug takes lowercase letters, digits and inner hyphens only")
		}
	}
	return nil
}

// Probes lists every probe, local one included, for the back-office.
func (s *Store) Probes() ([]Probe, error) {
	rows, err := s.cfg.Query(
		`SELECT id, slug, name, COALESCE(location,''), local, enabled,
		        COALESCE(last_seen_at,0), COALESCE(token_hash,'') <> '',
		        COALESCE(address,''), COALESCE(seen_address,'')
		   FROM probes ORDER BY local DESC, slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Probe{}
	for rows.Next() {
		var p Probe
		var local, enabled, hasTok int
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Location,
			&local, &enabled, &p.LastSeenAt, &hasTok, &p.Address, &p.SeenAddress); err != nil {
			return nil, err
		}
		p.Local, p.Enabled, p.HasToken = local == 1, enabled == 1, hasTok == 1
		p.From = p.EffectiveAddress()
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateProbe registers a probe that runs elsewhere. It is created without a
// token and therefore cannot send anything until one is issued, which keeps
// the two decisions — this probe exists, this probe may write — separate.
func (s *Store) CreateProbe(slug, name, location string) (*Probe, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if err := validProbeSlug(slug); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = slug
	}
	res, err := s.cfg.Exec(
		`INSERT INTO probes(slug,name,location,local,enabled) VALUES(?,?,?,0,1)`,
		slug, name, strings.TrimSpace(location))
	if err != nil {
		return nil, fmt.Errorf("a probe with this slug already exists")
	}
	id, _ := res.LastInsertId()
	return &Probe{ID: id, Slug: slug, Name: name,
		Location: strings.TrimSpace(location), Enabled: true}, nil
}

// IssueProbeToken mints a token for a probe and stores only its hash. The
// token is returned once; a second call replaces it, which is how a leaked
// token is rotated.
//
// It refuses the local probe on purpose. The local probe writes through the
// Unix socket, which is reachable only by a process on the host; giving it
// an HTTP credential as well would widen the surface for nothing.
func (s *Store) IssueProbeToken(id int64) (string, error) {
	var local int
	if err := s.cfg.QueryRow(`SELECT local FROM probes WHERE id=?`, id).Scan(&local); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("unknown probe")
		}
		return "", err
	}
	if local == 1 {
		return "", fmt.Errorf("the local probe writes through the Unix socket and takes no token")
	}
	tok := randomHex(24)
	if _, err := s.cfg.Exec(`UPDATE probes SET token_hash=? WHERE id=?`, hashToken(tok), id); err != nil {
		return "", err
	}
	return tok, nil
}

// RevokeProbeToken withdraws the credential. Rotation alone is not enough:
// a token that leaked from a decommissioned machine has to be removable
// without inventing a replacement nobody will use. ingest() refuses an
// empty hash, so this takes effect on the next request.
func (s *Store) RevokeProbeToken(id int64) error {
	res, err := s.cfg.Exec(`UPDATE probes SET token_hash=NULL WHERE id=? AND local=0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("unknown probe")
	}
	return nil
}

// SetProbeEnabled stops or resumes a probe. ingest() selects on enabled=1,
// so a disabled probe is refused at the door while its measurements stay.
//
// There is deliberately no delete. The rollup tables in metrics.db are keyed
// on probe_id and live in a different database, so removing the row here
// would orphan every measurement the probe ever sent, silently and with no
// way to attribute them again.
func (s *Store) SetProbeEnabled(id int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	res, err := s.cfg.Exec(`UPDATE probes SET enabled=? WHERE id=? AND local=0`, v, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("unknown probe")
	}
	return nil
}

// SeenProbeFrom records that a probe just spoke to us, and from where. It is
// the network path's version of TouchProbe: the local probe reaches the
// service through a Unix socket and has no remote address worth recording, so
// its own path is left exactly as it was.
//
// The address is written on every call rather than only when it changes,
// because the row is being updated for last_seen_at regardless and a
// comparison would cost a read to save nothing.
func (s *Store) SeenProbeFrom(id int64, addr string) {
	s.cfg.Exec(`UPDATE probes SET last_seen_at=?, seen_address=? WHERE id=?`,
		time.Now().Unix(), strings.TrimSpace(addr), id)
}

// SetProbeAddress states where a probe measures from, overriding what the
// service observes. An empty value clears the override and hands the question
// back to observation.
func (s *Store) SetProbeAddress(id int64, addr string) error {
	addr = strings.TrimSpace(addr)
	if addr != "" && net.ParseIP(addr) == nil {
		return fmt.Errorf("%q is not an IP address", addr)
	}
	res, err := s.cfg.Exec(`UPDATE probes SET address=? WHERE id=?`, addr, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("unknown probe")
	}
	return nil
}

// ProbeByToken resolves a probe from the token it presented. The lookup is on
// the hash rather than on the token, exactly as a share link resolves: what
// the database holds is not the secret, so comparing on it gives away nothing
// worth timing.
//
// A disabled probe resolves to nothing, which is what makes disabling it stop
// the measurements rather than merely hide it. So does the local probe, which
// has no token and must stay reachable through the socket alone.
func (s *Store) ProbeByToken(tok string) (int64, bool) {
	if len(tok) < 16 {
		return 0, false
	}
	var id int64
	err := s.cfg.QueryRow(
		`SELECT id FROM probes WHERE token_hash=? AND enabled=1 AND local=0`,
		hashToken(tok)).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

// ------------------------------------------------------------------ API

func (a *API) probesGet(w http.ResponseWriter, r *http.Request, u *User) {
	list, err := a.store.Probes()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// The AS is read from the lookup service's cache rather than stored, so a
	// probe that is renumbered is right again on its own. A miss schedules
	// the lookup and leaves the field empty for now; nothing here waits on
	// the network while an operator is looking at a page.
	if a.asn != nil {
		for i := range list {
			addr := list[i].From
			if addr == "" {
				continue
			}
			if as, ok := a.asn.ASNOfIP(addr); ok {
				list[i].ASN = as
			} else {
				go a.asn.RefreshIPASN(addr)
			}
		}
	}
	writeJSON(w, map[string]any{"probes": list})
}

func (a *API) probesPost(w http.ResponseWriter, r *http.Request, u *User) {
	var in struct {
		Slug     string `json:"slug"`
		Name     string `json:"name"`
		Location string `json:"location"`
		Address  string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Checked before the probe exists rather than after: a malformed address
	// should not leave a half-made probe behind for the operator to clean up.
	if addr := strings.TrimSpace(in.Address); addr != "" && net.ParseIP(addr) == nil {
		writeErr(w, 400, fmt.Sprintf("%q is not an IP address", addr))
		return
	}
	p, err := a.store.CreateProbe(in.Slug, in.Name, in.Location)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Address) != "" {
		if err := a.store.SetProbeAddress(p.ID, in.Address); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		p.Address = strings.TrimSpace(in.Address)
	}
	a.store.Audit(u, clientIP(r), "probe_create", "probe", p.Slug)
	writeJSON(w, p)
}

func (a *API) probeTokenPost(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad probe id")
		return
	}
	tok, err := a.store.IssueProbeToken(id)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a.store.Audit(u, clientIP(r), "probe_token_issue", "probe", strconv.FormatInt(id, 10))
	writeJSON(w, map[string]any{"token": tok})
}

func (a *API) probeTokenDelete(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad probe id")
		return
	}
	if err := a.store.RevokeProbeToken(id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a.store.Audit(u, clientIP(r), "probe_token_revoke", "probe", strconv.FormatInt(id, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) probesPatch(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad probe id")
		return
	}
	var in struct {
		Enabled *bool   `json:"enabled"`
		Address *string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.Enabled == nil && in.Address == nil {
		writeErr(w, 400, "nothing to change")
		return
	}
	// A pointer rather than a string, so that clearing the override is
	// expressible: an absent field changes nothing, an empty one hands the
	// question back to what the service observes.
	if in.Address != nil {
		if err := a.store.SetProbeAddress(id, *in.Address); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		a.store.Audit(u, clientIP(r), "probe_address", "probe", strconv.FormatInt(id, 10))
	}
	if in.Enabled != nil {
		if err := a.store.SetProbeEnabled(id, *in.Enabled); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		action := "probe_disable"
		if *in.Enabled {
			action = "probe_enable"
		}
		a.store.Audit(u, clientIP(r), action, "probe", strconv.FormatInt(id, 10))
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------------ CLI

// An operator on the host should not have to open the back-office to enrol a
// probe, if only because the first probe is often added while setting the
// machine up. These subcommands sit under "probe" beside the one that runs
// it, and go through exactly the same store calls as the API.
func cmdProbe(args []string) error {
	const usage = "usage: smokestack probe list|add|token|revoke [flags]"
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	fsn := flag.NewFlagSet("probe "+args[0], flag.ExitOnError)
	cfgPath := cliConfig(fsn)
	slug := fsn.String("slug", "", "probe slug")
	name := fsn.String("name", "", "display name")
	location := fsn.String("location", "", "where it measures from")
	address := fsn.String("address", "", "its public IP, when the observed one would be wrong (NAT, reverse proxy)")
	withToken := fsn.Bool("token", false, "issue a token straight away (add only)")
	fsn.Parse(args[1:])

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	store, err := OpenStore(cfg.DataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	// Resolving the slug here rather than in each branch keeps the three
	// commands that need an existing probe from repeating the lookup, and
	// gives all of them the same error for a slug nobody registered.
	bySlug := func() (int64, error) {
		if *slug == "" {
			return 0, fmt.Errorf("-slug is required")
		}
		var id int64
		err := store.cfg.QueryRow(`SELECT id FROM probes WHERE slug=?`, strings.TrimSpace(*slug)).Scan(&id)
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("no probe with slug %q", *slug)
		}
		return id, err
	}

	switch args[0] {
	case "list":
		list, err := store.Probes()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SLUG\tNAME\tLOCATION\tFROM\tKIND\tSTATE\tTOKEN")
		for _, p := range list {
			kind, state, tok := "remote", "disabled", "none"
			if p.Local {
				kind = "local"
			}
			if p.Enabled {
				state = "enabled"
			}
			if p.HasToken {
				tok = "set"
			} else if p.Local {
				tok = "n/a"
			}
			from := p.From
			if from == "" {
				from = "—"
			} else if p.Address != "" {
				from += " (stated)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.Slug, p.Name, p.Location, from, kind, state, tok)
		}
		return w.Flush()

	case "add":
		p, err := store.CreateProbe(*slug, *name, *location)
		if err != nil {
			return err
		}
		store.Audit(nil, "cli", "probe_create", "probe", p.Slug)
		if strings.TrimSpace(*address) != "" {
			if err := store.SetProbeAddress(p.ID, *address); err != nil {
				return err
			}
		}
		fmt.Printf("probe registered: %s\n", p.Slug)
		if !*withToken {
			fmt.Printf("it cannot send anything yet: run `smokestack probe token -slug %s` to issue one\n", p.Slug)
			return nil
		}
		tok, err := store.IssueProbeToken(p.ID)
		if err != nil {
			return err
		}
		store.Audit(nil, "cli", "probe_token_issue", "probe", strconv.FormatInt(p.ID, 10))
		printProbeToken(tok)
		return nil

	case "token":
		id, err := bySlug()
		if err != nil {
			return err
		}
		tok, err := store.IssueProbeToken(id)
		if err != nil {
			return err
		}
		store.Audit(nil, "cli", "probe_token_issue", "probe", strconv.FormatInt(id, 10))
		printProbeToken(tok)
		return nil

	case "revoke":
		id, err := bySlug()
		if err != nil {
			return err
		}
		if err := store.RevokeProbeToken(id); err != nil {
			return err
		}
		store.Audit(nil, "cli", "probe_token_revoke", "probe", strconv.FormatInt(id, 10))
		fmt.Printf("token revoked: %s can no longer send measurements\n", *slug)
		return nil
	}
	return fmt.Errorf("%s", usage)
}

// The token goes to stdout alone on its line, so that piping the command into
// a file or a secret store captures the token and nothing else. Everything
// said about it goes to stderr for the same reason.
func printProbeToken(tok string) {
	fmt.Fprintln(os.Stderr, "token issued. It is shown once and only its hash is stored, so copy it now:")
	fmt.Println(tok)
	fmt.Fprintln(os.Stderr, "set it as probe.token in the probe's configuration file.")
}
