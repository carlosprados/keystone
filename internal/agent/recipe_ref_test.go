package agent

import (
	"strings"
	"testing"
)

// TestResolveRecipeRefNamesBothLookups: a plan can reference a recipe by path
// or by store reference, and when both fail the message has to say so.
//
// Reporting only the path error sends the reader to look for a file that was
// never meant to exist — the exact failure this message is supposed to prevent.
// And because the separator is ":" while the widespread convention is "@", a
// reference written as name@version has to be told what the accepted form is,
// or it fails identically to a genuine typo.
func TestResolveRecipeRefNamesBothLookups(t *testing.T) {
	a := New(Options{InsecureSkipVerify: true})

	_, _, _, err := a.resolveRecipeRef("com.example.api@1.0.0")
	if err == nil {
		t.Fatal("expected an error for a reference that is neither a file nor in the store")
	}

	msg := err.Error()
	for _, want := range []string{"com.example.api@1.0.0", "recipe store", "name:version"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q: %s", want, msg)
		}
	}
}

// TestParseRecipeStoreRef pins the separator the message now advertises.
func TestParseRecipeStoreRef(t *testing.T) {
	name, version := parseRecipeStoreRef("com.example.api:1.0.0")
	if name != "com.example.api" || version != "1.0.0" {
		t.Errorf("got (%q, %q), want (com.example.api, 1.0.0)", name, version)
	}

	// No separator: the whole string is the name and any version will do.
	name, version = parseRecipeStoreRef("com.example.api")
	if name != "com.example.api" || version != "" {
		t.Errorf("got (%q, %q), want (com.example.api, \"\")", name, version)
	}

	// "@" is not the separator, which is why the error has to say so.
	name, version = parseRecipeStoreRef("com.example.api@1.0.0")
	if name != "com.example.api@1.0.0" || version != "" {
		t.Errorf("got (%q, %q), want the whole string as the name", name, version)
	}
}
