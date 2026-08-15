package dataset

import (
	"fmt"
	"strings"
	"time"

	"github.com/carlosprados/keystone/internal/recipe"
	"github.com/carlosprados/keystone/internal/validate"
)

// Defaults applied when a recipe leaves a field out.
const (
	DefaultRefresh = 24 * time.Hour
	DefaultKeep    = 2
	DefaultGrace   = 30 * time.Second
	maxAgeMultiple = 3 // max_age defaults to three missed refreshes
	minimumRefresh = time.Minute
)

// Spec is a validated, defaulted [[datasets]] entry.
type Spec struct {
	Name        string
	Manifest    string
	SigURI      string
	CertURI     string
	Refresh     time.Duration
	MaxAge      time.Duration
	Keep        int
	Required    bool
	Headers     map[string]string
	GithubToken string
}

// ParseSpec validates one recipe entry and fills in the defaults.
//
// It is strict about the name because it becomes a directory and an environment
// variable: the same allow-list recipe names already use, so a dataset cannot
// traverse out of runtime/datasets.
func ParseSpec(d recipe.Dataset) (Spec, error) {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return Spec{}, fmt.Errorf("dataset has no name")
	}
	if err := validate.ValidatePathSegment("dataset name", name); err != nil {
		return Spec{}, err
	}
	if strings.TrimSpace(d.Manifest) == "" {
		return Spec{}, fmt.Errorf("dataset %q has no manifest URL", name)
	}

	refresh, err := parseDuration(d.Refresh, DefaultRefresh)
	if err != nil {
		return Spec{}, fmt.Errorf("dataset %q refresh: %w", name, err)
	}
	if refresh < minimumRefresh {
		return Spec{}, fmt.Errorf("dataset %q refresh %s is below the %s minimum: a dataset is not a poll loop", name, refresh, minimumRefresh)
	}

	maxAge, err := parseDuration(d.MaxAge, maxAgeMultiple*refresh)
	if err != nil {
		return Spec{}, fmt.Errorf("dataset %q max_age: %w", name, err)
	}
	if maxAge < refresh {
		return Spec{}, fmt.Errorf("dataset %q max_age %s is shorter than its refresh %s, so it would report stale between every update", name, maxAge, refresh)
	}

	keep := d.Keep
	if keep == 0 {
		keep = DefaultKeep
	}
	if keep < 2 {
		return Spec{}, fmt.Errorf("dataset %q keep=%d leaves nothing to roll back to", name, keep)
	}

	required := true
	if d.Required != nil {
		required = *d.Required
	}

	sig := strings.TrimSpace(d.SigURI)
	if sig == "" {
		sig = strings.TrimSpace(d.Manifest) + ".sig"
	}

	return Spec{
		Name:        name,
		Manifest:    strings.TrimSpace(d.Manifest),
		SigURI:      sig,
		CertURI:     strings.TrimSpace(d.CertURI),
		Refresh:     refresh,
		MaxAge:      maxAge,
		Keep:        keep,
		Required:    required,
		Headers:     d.Headers,
		GithubToken: d.GithubToken,
	}, nil
}

// ParseSpecs validates every dataset in a recipe, rejecting duplicates: two
// entries with one name would fight over the same directory and the same
// environment variable.
func ParseSpecs(ds []recipe.Dataset) ([]Spec, error) {
	out := make([]Spec, 0, len(ds))
	seen := map[string]bool{}
	for _, d := range ds {
		spec, err := ParseSpec(d)
		if err != nil {
			return nil, err
		}
		if seen[spec.Name] {
			return nil, fmt.Errorf("duplicate dataset name %q", spec.Name)
		}
		seen[spec.Name] = true
		out = append(out, spec)
	}
	return out, nil
}

// ReloadPlan is how a component is told its data changed.
type ReloadPlan struct {
	Signal string
	Script string
	Grace  time.Duration
}

// ParseReload validates [lifecycle.reload].
//
// A container reports no PID, so a signal cannot reach it; saying so is better
// than accepting a reload that quietly does nothing and leaves the component on
// stale data.
func ParseReload(r recipe.LifecycleReload, runType string) (ReloadPlan, error) {
	signal := strings.TrimSpace(r.Signal)
	script := strings.TrimSpace(r.Script)

	if signal != "" && script != "" {
		return ReloadPlan{}, fmt.Errorf("[lifecycle.reload] declares both signal and script; pick one")
	}
	if signal != "" && runType == "container" {
		return ReloadPlan{}, fmt.Errorf("[lifecycle.reload] signal is not available for a container component (it has no PID to signal); use script")
	}
	if signal != "" {
		if _, err := ParseSignal(signal); err != nil {
			return ReloadPlan{}, err
		}
	}

	grace, err := parseDuration(r.Grace, DefaultGrace)
	if err != nil {
		return ReloadPlan{}, fmt.Errorf("[lifecycle.reload] grace: %w", err)
	}
	return ReloadPlan{Signal: signal, Script: script, Grace: grace}, nil
}

// Declared reports whether the recipe asked for any reload at all.
func (r ReloadPlan) Declared() bool { return r.Signal != "" || r.Script != "" }

func parseDuration(s string, def time.Duration) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (try \"24h\", \"30m\"): %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be positive", s)
	}
	return d, nil
}
