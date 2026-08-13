package validate

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// DecodeTOML decodes a TOML document into v and reports, without failing, every
// key the document carries that v does not declare.
//
// Two failure modes pull in opposite directions here, and this signature is how
// the codebase holds both:
//
//   - A typo is silent and changes behaviour. `restart_polciy = "never"` used to
//     parse cleanly and leave RestartPolicy empty, and an empty restart policy
//     resolves to "always" — a component the author asked never to restart
//     restarted forever, with no diagnostic. The same holds for a key written
//     under the wrong table header, which TOML makes easy.
//   - Rejecting unknown keys outright breaks a mixed-version fleet. One recipe
//     is published to many agents; an agent that predates a field must ignore
//     it, not refuse the recipe. That property is pinned by
//     TestUnmarshalIgnoresUnknownFields and it is why [artifacts.delta] could
//     ship at all.
//
// So the decode never fails on an unknown key: it returns them. Callers decide.
// The authoring path — a dry-run apply — turns the list into an error, where it
// costs one edit. The runtime path logs it and carries on, where refusing would
// strand a device over a field its agent is simply too old to know about.
//
// The struct tags are the source of truth, deliberately. The alternative was
// "additionalProperties": false on the JSON Schemas in this package, which would
// mean hand-maintaining a second list of every field — and a schema that forgets
// a field rejects a valid recipe, which is worse than the bug being fixed. Here
// the accepted set cannot drift from the set the program actually reads.
//
// Each entry is rendered as `"lifecycle.run.restart_polciy" (line 6)`: the key
// alone is not enough to find it in a file that repeats [[artifacts]] four times.
func DecodeTOML(b []byte, v any) (unknown []string, err error) {
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()

	// A strict decode still populates v completely; the unknown keys come back
	// as an error alongside a fully decoded value. Verified, not assumed — one
	// pass is enough, and a second lax decode would only invite the two to
	// disagree.
	err = dec.Decode(v)
	if err == nil {
		return nil, nil
	}

	var strict *toml.StrictMissingError
	if !errors.As(err, &strict) {
		return nil, err
	}

	seen := make(map[string]struct{}, len(strict.Errors))
	for i := range strict.Errors {
		key := strings.Join(strict.Errors[i].Key(), ".")
		row, _ := strict.Errors[i].Position()
		desc := fmt.Sprintf("%q (line %d)", key, row)
		if _, dup := seen[desc]; dup {
			continue
		}
		seen[desc] = struct{}{}
		unknown = append(unknown, desc)
	}
	sort.Strings(unknown)
	return unknown, nil
}

// UnknownFieldsError renders a list from DecodeTOML as the error the authoring
// path returns. Kept next to the decoder so both sides word it identically.
func UnknownFieldsError(unknown []string) error {
	if len(unknown) == 0 {
		return nil
	}
	noun := "unknown field"
	if len(unknown) > 1 {
		noun = "unknown fields"
	}
	return fmt.Errorf("%s: %s", noun, strings.Join(unknown, ", "))
}
