package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.1.9", 1}, {"v1.2.0", "1.2.0", 0}, {"1.10.0", "1.9.0", 1},
		{"1.2.0-rc1", "1.2.0", -1}, {"1.2.0", "1.2.0-rc1", 1}, {"0.1.0", "dev", 1},
		{"2.0.0", "10.0.0", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%s,%s)=%d, attendu %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"", "dev", "../1.0", "1.0/x", "v"} {
		if validVersion(bad) {
			t.Errorf("version %q acceptee a tort", bad)
		}
	}
}

func TestClassify(t *testing.T) {
	// A zero Thresholds means "instance defaults", which are the values that
	// were hardcoded before they could be set: nothing moves for an instance
	// that never touches them.
	var d Thresholds
	if classify(0, 0, 0, 0, d) != "nodata" || classify(100, 5, 10, 10, d) != "crit" ||
		classify(1000, 5, 10, 10, d) != "warn" || classify(100, 0, 20, 10, d) != "warn" ||
		classify(100, 0, 0.14, 0.09, d) != "ok" || classify(100, 0, 11, 10, d) != "ok" {
		t.Error("default state rules are wrong")
	}
}

// A target known to be badly connected can accept more loss, and a target
// that matters can be watched more closely than the rest.
func TestClassifyPerTargetThresholds(t *testing.T) {
	// 5 % loss: critical by default, still fine on a link declared poor.
	lax := Thresholds{LossWarn: 8, LossCrit: 20}
	if got := classify(100, 5, 10, 10, lax); got != "ok" {
		t.Errorf("a tolerant target should stay ok at 5%% loss, got %q", got)
	}
	if got := classify(100, 25, 10, 10, lax); got != "crit" {
		t.Errorf("even a tolerant target turns critical at 25%%, got %q", got)
	}
	// The opposite: a link that must not lose anything.
	strict := Thresholds{LossWarn: 0.1, LossCrit: 0.5}
	if got := classify(1000, 3, 10, 10, strict); got != "warn" {
		t.Errorf("a strict target should warn at 0.3%% loss, got %q", got)
	}
	// Latency: a factor of 3 on a jittery path, where 1.4 would shout.
	if got := classify(100, 0, 20, 10, Thresholds{LatFactor: 3}); got != "ok" {
		t.Errorf("twice the baseline is fine at factor 3, got %q", got)
	}
	if got := classify(100, 0, 40, 10, Thresholds{LatFactor: 3}); got != "warn" {
		t.Errorf("four times the baseline exceeds factor 3, got %q", got)
	}
	// An absolute move of less than a millisecond never raises anything,
	// whatever the factor: 40 % more than 0.2 ms is still 0.2 ms.
	if got := classify(100, 0, 0.3, 0.1, Thresholds{LatFactor: 1.1}); got != "ok" {
		t.Errorf("a negligible absolute rise must stay ok, got %q", got)
	}
	// A target setting only one value keeps the instance default for the
	// other, and an incoherent pair is repaired rather than refused.
	m := Thresholds{LossWarn: 5}.merge(DefaultThresholds())
	if m.LossWarn != 5 || m.LossCrit < m.LossWarn {
		t.Errorf("merge with the defaults: %+v", m)
	}
}

// makePackage fabrique un paquet de test, signe ou non, eventuellement altere.
func makePackage(t *testing.T, dir string, priv ed25519.PrivateKey, tamper bool) string {
	bin := []byte("#!/bin/sh\necho binaire\n")
	sum := sha256.Sum256(bin)
	m := Manifest{Name: "smokestack", Version: "9.9.9", OS: runtime.GOOS, Arch: runtime.GOARCH,
		SHA256: hex.EncodeToString(sum[:])}
	raw, _ := json.Marshal(m)
	p := filepath.Join(dir, "pkg.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("smokestack")
	if tamper {
		bin = append(bin, '!')
	}
	w.Write(bin)
	w, _ = zw.Create("manifest.json")
	w.Write(raw)
	if priv != nil {
		w, _ = zw.Create("manifest.sig")
		w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))))
	}
	zw.Close()
	f.Close()
	return p
}

func TestVerifyPackage(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	u := &Updater{keys: map[string]ed25519.PublicKey{keyID(pub): pub}}
	dir := t.TempDir()

	st, zr, err := u.Verify(makePackage(t, dir, priv, false), true)
	if err != nil || !st.Signed || !st.Newer {
		t.Fatalf("paquet valide refuse : %v", err)
	}
	zr.Close()
	if _, _, err := u.Verify(makePackage(t, dir, other, false), true); err == nil {
		t.Error("paquet signe par une cle inconnue accepte")
	}
	if _, _, err := u.Verify(makePackage(t, dir, nil, false), false); err == nil {
		t.Error("paquet non signe accepte sans allow_unsigned")
	}
	u.cfg.AllowUnsigned = true
	if _, zr, err := u.Verify(makePackage(t, dir, nil, false), false); err != nil {
		t.Errorf("paquet non signe refuse malgre allow_unsigned : %v", err)
	} else {
		zr.Close()
	}
	if _, _, err := u.Verify(makePackage(t, dir, nil, false), true); err == nil {
		t.Error("la mise a jour automatique doit toujours exiger une signature")
	}
}

func TestSetupCode(t *testing.T) {
	c := newSetupCode()
	if len(c) != 14 || strings.Count(c, "-") != 2 || strings.ContainsAny(c, "01IO") {
		t.Errorf("code d'installation mal forme : %s", c)
	}
}

func TestCheckEvery(t *testing.T) {
	u := &Updater{cfg: UpdateConfig{CheckHours: 0}}
	if u.checkEvery() != 24*time.Hour {
		t.Errorf("without a setting: %v, expected 24 h", u.checkEvery())
	}
	u.cfg.CheckHours = 168
	if u.checkEvery() != 168*time.Hour {
		t.Errorf("weekly: %v", u.checkEvery())
	}
	u.cfg.CheckHours = 100000 // clamped to one month
	if u.checkEvery() != 720*time.Hour {
		t.Errorf("clamping: %v", u.checkEvery())
	}
	u.cfg.CheckHours = -3 // invalid: back to the default
	if u.checkEvery() != 24*time.Hour {
		t.Errorf("invalid value: %v", u.checkEvery())
	}
}

// Skipping versions must be harmless: the newest release is installed
// directly, whatever the current version.
func TestVersionSkipping(t *testing.T) {
	for _, c := range []struct{ from, latest string }{{"0.1.0", "0.1.4"}, {"0.0.9", "2.0.0"}, {"1.9.9", "1.10.0"}} {
		if compareVersions(c.latest, c.from) <= 0 {
			t.Errorf("%s should be newer than %s", c.latest, c.from)
		}
	}
	if compareVersions("0.1.4", "0.1.4") != 0 || compareVersions("0.1.3", "0.1.4") >= 0 {
		t.Error("comparison of close versions")
	}
}

// Un operateur doit pouvoir ne faire confiance qu'a ses propres cles de
// signature, plutot que d'ajouter les siennes a celle du projet.
func TestExclusiveReleaseKeys(t *testing.T) {
	dir := t.TempDir()
	pub, _, _ := ed25519.GenerateKey(nil)
	path := filepath.Join(dir, "keys.pub")
	os.WriteFile(path, []byte("exclusive\ned25519:"+
		base64.StdEncoding.EncodeToString(pub)+"\n"), 0o600)

	u := &Updater{cfg: UpdateConfig{TrustedKeys: path},
		keys: map[string]ed25519.PublicKey{}}
	u.loadKeys()
	if len(u.keys) != 1 {
		t.Fatalf("%d keys kept, expected only the operator's", len(u.keys))
	}
	if _, ok := u.keys[keyID(pub)]; !ok {
		t.Error("the operator's key should be the one kept")
	}

	// Sans la directive, les cles s'ajoutent a celles du binaire.
	os.WriteFile(path, []byte("ed25519:"+
		base64.StdEncoding.EncodeToString(pub)+"\n"), 0o600)
	u2 := &Updater{cfg: UpdateConfig{TrustedKeys: path},
		keys: map[string]ed25519.PublicKey{}}
	u2.loadKeys()
	if len(u2.keys) < 2 {
		t.Errorf("%d keys, the embedded one should still be there", len(u2.keys))
	}
}
