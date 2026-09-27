package enrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Files inside a generation.
const (
	KeyFile      = "device.key"
	CertFile     = "device.crt"
	BrokerCAFile = "broker-ca.pem"
	MetaFile     = "enrolment.json"
	currentLink  = "current"
	genPrefix    = "gen-"
)

// Enrolment is what the agent needs to run with, and to renew, an identity.
type Enrolment struct {
	Version    int       `json:"version"`
	EnrolURL   string    `json:"enrolUrl"`
	CAPin      string    `json:"caPin"`
	Tenant     string    `json:"tenant"`
	Device     string    `json:"device"`
	NotAfter   time.Time `json:"notAfter"`
	RenewAfter time.Time `json:"renewAfter"`
}

// Store is a directory of generations and a "current" link to one of them.
//
// Key, certificate and broker CA are swapped together by replacing one
// symlink, so no reader ever sees a new key with an old certificate. A write
// that is interrupted leaves a half-built generation nobody points at.
type Store struct{ Dir string }

// Paths the agent configures MQTT with. They go through "current", so they
// name the new files the moment a renewal swaps the link.
func (s Store) KeyPath() string      { return filepath.Join(s.Dir, currentLink, KeyFile) }
func (s Store) CertPath() string     { return filepath.Join(s.Dir, currentLink, CertFile) }
func (s Store) BrokerCAPath() string { return filepath.Join(s.Dir, currentLink, BrokerCAFile) }

// ErrNotEnrolled means the directory holds no identity yet.
var ErrNotEnrolled = errors.New("not enrolled")

// Current reads the enrolment in force.
func (s Store) Current() (*Enrolment, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, currentLink, MetaFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: no identity in %s (run keystone enrol)", ErrNotEnrolled, s.Dir)
	}
	if err != nil {
		return nil, err
	}
	var e Enrolment
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("%s: %w", MetaFile, err)
	}
	return &e, nil
}

// write stores a new generation and makes it current.
func (s Store) write(keyPEM []byte, iss *Issued, meta Enrolment) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(s.Dir, ".incoming-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // a no-op once renamed

	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{KeyFile, keyPEM, 0o600},
		{CertFile, []byte(iss.Certificate), 0o644},
		{BrokerCAFile, []byte(iss.BrokerCA), 0o644},
		{MetaFile, metaJSON, 0o644},
	}
	for _, f := range files {
		if err := writeSynced(filepath.Join(tmp, f.name), f.data, f.mode); err != nil {
			return err
		}
	}
	if err := os.Chmod(tmp, 0o700); err != nil {
		return err
	}

	gen := genPrefix + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.Rename(tmp, filepath.Join(s.Dir, gen)); err != nil {
		return err
	}
	link := filepath.Join(s.Dir, currentLink)
	if err := os.Symlink(gen, link+".tmp"); err != nil {
		return err
	}
	if err := os.Rename(link+".tmp", link); err != nil {
		_ = os.Remove(link + ".tmp")
		return err
	}
	syncDir(s.Dir)
	s.prune(gen)
	return nil
}

// prune keeps the current generation and the one before it: the previous
// key and certificate are still valid until they expire, and are what an
// operator reaches for if the new one turns out to be wrong.
func (s Store) prune(current string) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	var gens []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), genPrefix) && e.Name() != current {
			gens = append(gens, e.Name())
		}
	}
	sort.Strings(gens)
	for i := 0; i < len(gens)-1; i++ {
		_ = os.RemoveAll(filepath.Join(s.Dir, gens[i]))
	}
}

func writeSynced(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
