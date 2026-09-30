package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Maintenances programmees. Une cible qu'on redemarre volontairement
// n'est pas une cible en panne, et un graphe qui ne le dit pas raconte
// une fausse histoire deux fois : il reveille l'astreinte pour rien, et
// il laisse un trou que personne ne saura expliquer six mois plus tard.
//
// Deux interrupteurs distincts, parce que les deux besoins existent :
// couper l'alerting en continuant a mesurer (on veut voir ce qui se
// passe, sans etre reveille) ou couper aussi la mesure (le redemarrage
// ferait apparaitre une perte de cent pour cent qui polluerait les
// statistiques et la disponibilite).

const maintenanceSchema = `
CREATE TABLE IF NOT EXISTS maintenances (
  id          INTEGER PRIMARY KEY,
  target_id   INTEGER NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
  title       TEXT NOT NULL,
  note        TEXT NOT NULL DEFAULT '',
  starts_at   INTEGER NOT NULL,
  ends_at     INTEGER NOT NULL,
  stop_alerts INTEGER NOT NULL DEFAULT 1,
  stop_probe  INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  created_by  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_maint_target ON maintenances(target_id, starts_at);
CREATE INDEX IF NOT EXISTS idx_maint_window ON maintenances(starts_at, ends_at);
`

// maxMaintenanceSpan : une fenetre de maintenance est un evenement, pas
// un etat permanent. Une cible qu'on arrete indefiniment se desactive ;
// une maintenance de trois mois est une erreur de saisie.
const maxMaintenanceSpan = 30 * 86400

type Maintenance struct {
	ID       int64  `json:"id"`
	TargetID int64  `json:"target_id"`
	Title    string `json:"title"`
	Note     string `json:"note,omitempty"`
	StartsAt int64  `json:"starts_at"`
	EndsAt   int64  `json:"ends_at"`
	// StopAlerts laisse la mesure tourner et retient seulement les
	// notifications. StopProbe arrete aussi la mesure.
	StopAlerts bool   `json:"stop_alerts"`
	StopProbe  bool   `json:"stop_probe"`
	CreatedAt  int64  `json:"created_at,omitempty"`
	CreatedBy  string `json:"created_by,omitempty"`
	// Champs de commodite pour l'interface, jamais stockes.
	TargetTitle string `json:"target_title,omitempty"`
	TargetSlug  string `json:"target_slug,omitempty"`
}

func (m *Maintenance) active(now int64) bool {
	return now >= m.StartsAt && now < m.EndsAt
}

func (m *Maintenance) check() error {
	if strings.TrimSpace(m.Title) == "" {
		return fmt.Errorf("a maintenance window needs a title: it is what the public banner shows")
	}
	if m.EndsAt <= m.StartsAt {
		return fmt.Errorf("the window ends before it starts")
	}
	if m.EndsAt-m.StartsAt > maxMaintenanceSpan {
		return fmt.Errorf("a maintenance window cannot exceed %d days; disable the target instead",
			maxMaintenanceSpan/86400)
	}
	if !m.StopAlerts && !m.StopProbe {
		return fmt.Errorf("a window that stops neither alerting nor measurement does nothing")
	}
	m.Title = oneLine(m.Title, 120)
	m.Note = oneLine(m.Note, 400)
	return nil
}

func (s *Store) CreateMaintenance(m *Maintenance) (int64, error) {
	if err := m.check(); err != nil {
		return 0, err
	}
	if _, err := s.TargetByID(m.TargetID); err != nil {
		return 0, fmt.Errorf("unknown target")
	}
	res, err := s.cfg.Exec(
		`INSERT INTO maintenances(target_id,title,note,starts_at,ends_at,
		                          stop_alerts,stop_probe,created_at,created_by)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		m.TargetID, m.Title, m.Note, m.StartsAt, m.EndsAt,
		m.StopAlerts, m.StopProbe, time.Now().Unix(), m.CreatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateMaintenance(m *Maintenance) error {
	if err := m.check(); err != nil {
		return err
	}
	_, err := s.cfg.Exec(
		`UPDATE maintenances SET title=?,note=?,starts_at=?,ends_at=?,
		        stop_alerts=?,stop_probe=? WHERE id=?`,
		m.Title, m.Note, m.StartsAt, m.EndsAt, m.StopAlerts, m.StopProbe, m.ID)
	return err
}

func (s *Store) DeleteMaintenance(id int64) error {
	_, err := s.cfg.Exec(`DELETE FROM maintenances WHERE id=?`, id)
	return err
}

// Maintenances liste les fenetres d'une cible, ou de toutes si targetID
// vaut zero, de la plus recente a la plus ancienne. from borne la
// requete : zero prend tout.
func (s *Store) Maintenances(targetID, from int64) []*Maintenance {
	q := `SELECT m.id,m.target_id,m.title,m.note,m.starts_at,m.ends_at,
	             m.stop_alerts,m.stop_probe,m.created_at,m.created_by,
	             COALESCE(t.title,''),COALESCE(t.slug,'')
	        FROM maintenances m LEFT JOIN targets t ON t.id=m.target_id
	       WHERE m.ends_at>=?`
	args := []any{from}
	if targetID > 0 {
		q += ` AND m.target_id=?`
		args = append(args, targetID)
	}
	q += ` ORDER BY m.starts_at DESC LIMIT 500`
	rows, err := s.cfg.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Maintenance
	for rows.Next() {
		m := &Maintenance{}
		if rows.Scan(&m.ID, &m.TargetID, &m.Title, &m.Note, &m.StartsAt, &m.EndsAt,
			&m.StopAlerts, &m.StopProbe, &m.CreatedAt, &m.CreatedBy,
			&m.TargetTitle, &m.TargetSlug) != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ActiveMaintenances renvoie, par cible, la fenetre en cours. Une seule
// requete : le planificateur l'appelle toutes les dix secondes et les
// pages publiques a chaque construction de l'apercu.
func (s *Store) ActiveMaintenances(now int64) map[int64]*Maintenance {
	out := map[int64]*Maintenance{}
	rows, err := s.cfg.Query(
		`SELECT id,target_id,title,note,starts_at,ends_at,stop_alerts,stop_probe
		   FROM maintenances WHERE starts_at<=? AND ends_at>?`, now, now)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		m := &Maintenance{}
		if rows.Scan(&m.ID, &m.TargetID, &m.Title, &m.Note, &m.StartsAt, &m.EndsAt,
			&m.StopAlerts, &m.StopProbe) != nil {
			continue
		}
		// Deux fenetres qui se chevauchent sur une meme cible : on garde
		// la plus stricte, sinon une fenetre permissive annulerait une
		// fenetre qui coupe la mesure.
		if prev, ok := out[m.TargetID]; ok {
			m.StopAlerts = m.StopAlerts || prev.StopAlerts
			m.StopProbe = m.StopProbe || prev.StopProbe
			if prev.EndsAt > m.EndsAt {
				m.EndsAt = prev.EndsAt
			}
		}
		out[m.TargetID] = m
	}
	return out
}

// maintenanceRanges renvoie les intervalles qui coupent la mesure sur une
// cible et qui touchent [from, to]. Utilise par la disponibilite : une
// cible arretee volontairement ne compte ni comme disponible ni comme
// indisponible, elle ne compte pas.
func (s *Store) maintenanceRanges(targetID, from, to int64) [][2]int64 {
	rows, err := s.cfg.Query(
		`SELECT starts_at,ends_at FROM maintenances
		  WHERE target_id=? AND stop_probe=1 AND starts_at<? AND ends_at>?
		  ORDER BY starts_at`, targetID, to, from)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out [][2]int64
	for rows.Next() {
		var a, b int64
		if rows.Scan(&a, &b) != nil {
			continue
		}
		out = append(out, [2]int64{a, b})
	}
	return mergeRanges(out)
}

// mergeRanges fusionne les intervalles qui se touchent, pour ne pas
// soustraire deux fois la meme periode.
func mergeRanges(in [][2]int64) [][2]int64 {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i][0] < in[j][0] })
	out := [][2]int64{in[0]}
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		if r[0] <= last[1] {
			if r[1] > last[1] {
				last[1] = r[1]
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// ------------------------------------------------------------------ API

func (a *API) MaintenanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/maintenance", a.maintenancePublic)
	mux.HandleFunc("GET /api/v1/admin/maintenance", a.auth(a.maintenanceList))
	mux.HandleFunc("POST /api/v1/admin/maintenance", a.need(RoleAdmin, a.maintenanceCreate))
	mux.HandleFunc("PUT /api/v1/admin/maintenance/{id}", a.auth(a.maintenanceUpdate))
	mux.HandleFunc("DELETE /api/v1/admin/maintenance/{id}", a.auth(a.maintenanceDelete))
}

// maintenancePublic sert la banniere : ce qui est en cours, et ce qui est
// annonce pour les prochains jours, sur les cibles publiques seulement.
// Le titre et la note sont ecrits pour etre lus par un visiteur, donc ils
// sortent tels quels ; c'est a l'operateur de ne pas y mettre le nom de
// son fournisseur s'il ne veut pas le publier, et l'interface le dit.
func (a *API) maintenancePublic(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	authed := a.authenticated(r)
	// Cache court, et separe pour l'appelant authentifie qui voit aussi
	// les cibles privees : la banniere est sur la page d'accueil, donc
	// chaque visiteur passerait ici sinon.
	ck := "maint|pub"
	if authed {
		ck = "maint|auth"
	}
	if v := availCache.get(ck); v != nil {
		writeJSON(w, v)
		return
	}
	ahead := now + 7*86400
	var out []*Maintenance
	for _, m := range a.store.Maintenances(0, now) {
		if m.StartsAt > ahead {
			continue
		}
		t, err := a.store.TargetByID(m.TargetID)
		if err != nil || (!t.Public && !authed) {
			continue
		}
		m.CreatedBy, m.CreatedAt = "", 0
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt < out[j].StartsAt })
	res := map[string]any{"now": now, "maintenances": out}
	// Trente secondes : une fenetre qui s'ouvre doit apparaitre vite, un
	// visiteur n'attend pas la minute suivante pour comprendre un arret.
	availCache.put(ck, res, 30*time.Second)
	writeJSON(w, res)
}

func (a *API) maintenanceList(w http.ResponseWriter, r *http.Request) {
	var targetID int64
	if v := r.URL.Query().Get("target"); v != "" {
		targetID, _ = strconv.ParseInt(v, 10, 64)
	}
	// Les fenetres passees restent visibles un temps : c'est ce qui
	// permet d'expliquer un trou dans un graphe apres coup.
	from := time.Now().Unix() - 180*86400
	writeJSON(w, map[string]any{"maintenances": a.store.Maintenances(targetID, from)})
}

func (a *API) maintenanceCreate(w http.ResponseWriter, r *http.Request, u *User) {
	var m Maintenance
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if u != nil {
		m.CreatedBy = oneLine(u.Email, 120)
	}
	id, err := a.store.CreateMaintenance(&m)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	m.ID = id
	writeJSON(w, m)
}

func (a *API) maintenanceUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	var m Maintenance
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	m.ID = id
	if err := a.store.UpdateMaintenance(&m); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, m)
}

func (a *API) maintenanceDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	if err := a.store.DeleteMaintenance(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.WriteHeader(204)
}
