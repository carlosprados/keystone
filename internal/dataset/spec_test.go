package dataset

import (
	"strings"
	"testing"
	"time"

	"github.com/carlosprados/keystone/internal/recipe"
)

func TestParseSpecDefaults(t *testing.T) {
	spec, err := ParseSpec(recipe.Dataset{
		Name:     "cve-bundle",
		Manifest: "https://hub.plant.local/cve.manifest.toml",
	})
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Refresh != DefaultRefresh {
		t.Errorf("refresh=%s, want %s", spec.Refresh, DefaultRefresh)
	}
	if spec.MaxAge != maxAgeMultiple*DefaultRefresh {
		t.Errorf("max_age=%s, want three missed refreshes", spec.MaxAge)
	}
	if spec.Keep != DefaultKeep {
		t.Errorf("keep=%d, want %d", spec.Keep, DefaultKeep)
	}
	if !spec.Required {
		t.Error("a dataset is required by default: a discovery product with no OUI list is wrong, not degraded")
	}
	if spec.SigURI != "https://hub.plant.local/cve.manifest.toml.sig" {
		t.Errorf("sig_uri=%q, want the manifest with .sig appended", spec.SigURI)
	}
}

func TestParseSpecRejects(t *testing.T) {
	cases := []struct {
		name string
		in   recipe.Dataset
		want string
	}{
		{"no name", recipe.Dataset{Manifest: "https://x/m.toml"}, "no name"},
		{"no manifest", recipe.Dataset{Name: "oui"}, "no manifest"},
		{"traversing name", recipe.Dataset{Name: "../../etc", Manifest: "https://x/m.toml"}, "dataset name"},
		{"refresh too short", recipe.Dataset{Name: "oui", Manifest: "https://x/m.toml", Refresh: "5s"}, "not a poll loop"},
		{"unparseable refresh", recipe.Dataset{Name: "oui", Manifest: "https://x/m.toml", Refresh: "daily"}, "is not a duration"},
		{"max_age below refresh", recipe.Dataset{Name: "oui", Manifest: "https://x/m.toml", Refresh: "24h", MaxAge: "1h"}, "shorter than its refresh"},
		{"keep of one", recipe.Dataset{Name: "oui", Manifest: "https://x/m.toml", Keep: 1}, "nothing to roll back to"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseSpec(c.in)
			if err == nil {
				t.Fatal("accepted an unusable dataset")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// Two datasets with one name would fight over the same directory and the same
// environment variable.
func TestParseSpecsRejectsDuplicates(t *testing.T) {
	_, err := ParseSpecs([]recipe.Dataset{
		{Name: "oui", Manifest: "https://a/m.toml"},
		{Name: "oui", Manifest: "https://b/m.toml"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("err=%v, want a duplicate-name rejection", err)
	}
}

func TestParseSpecRequiredCanBeTurnedOff(t *testing.T) {
	no := false
	spec, err := ParseSpec(recipe.Dataset{Name: "oui", Manifest: "https://x/m.toml", Required: &no})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Required {
		t.Error("required=false was ignored")
	}
}

func TestParseReload(t *testing.T) {
	r, err := ParseReload(recipe.LifecycleReload{Signal: "SIGHUP"}, "process")
	if err != nil {
		t.Fatalf("ParseReload: %v", err)
	}
	if !r.Declared() || r.Signal != "SIGHUP" {
		t.Errorf("plan=%+v", r)
	}
	if r.Grace != DefaultGrace {
		t.Errorf("grace=%s, want %s", r.Grace, DefaultGrace)
	}

	if plan, err := ParseReload(recipe.LifecycleReload{}, "process"); err != nil || plan.Declared() {
		t.Errorf("an absent reload block must parse to nothing declared: %+v %v", plan, err)
	}
}

// A container has no PID to signal. Saying so beats accepting a reload that
// silently does nothing and leaves the component on stale data.
func TestParseReloadRejectsSignalOnAContainer(t *testing.T) {
	_, err := ParseReload(recipe.LifecycleReload{Signal: "SIGHUP"}, "container")
	if err == nil || !strings.Contains(err.Error(), "container") {
		t.Errorf("err=%v, want a rejection naming the container case", err)
	}
	// A script is how a container reloads.
	if _, err := ParseReload(recipe.LifecycleReload{Script: "docker kill -s HUP app"}, "container"); err != nil {
		t.Errorf("a container reload script was rejected: %v", err)
	}
}

func TestParseReloadRejectsBothAtOnce(t *testing.T) {
	_, err := ParseReload(recipe.LifecycleReload{Signal: "SIGHUP", Script: "true"}, "process")
	if err == nil || !strings.Contains(err.Error(), "pick one") {
		t.Errorf("err=%v", err)
	}
}

// The allow-list is the point: a reload is meant to make a component reread a
// file, and SIGKILL would turn "your data changed" into an outage.
func TestParseSignalAllowList(t *testing.T) {
	for _, ok := range []string{"SIGHUP", "hup", "SIGUSR1", "usr2"} {
		if _, err := ParseSignal(ok); err != nil {
			t.Errorf("ParseSignal(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"SIGKILL", "SIGTERM", "KILL", "9", ""} {
		if _, err := ParseSignal(bad); err == nil {
			t.Errorf("ParseSignal(%q) was accepted", bad)
		}
	}
}

func TestParseReloadGrace(t *testing.T) {
	r, err := ParseReload(recipe.LifecycleReload{Signal: "SIGHUP", Grace: "2m"}, "process")
	if err != nil {
		t.Fatal(err)
	}
	if r.Grace != 2*time.Minute {
		t.Errorf("grace=%s", r.Grace)
	}
}
