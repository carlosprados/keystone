package validate

import (
    "io"

    "github.com/santhosh-tekuri/jsonschema/v5"
)

// ValidateJSON validates an object (already converted to JSON) with the given schema.
func ValidateJSON(obj any, schemaSrc string) error {
    c := jsonschema.NewCompiler()
    if err := c.AddResource("mem://schema.json", bytesReader(schemaSrc)); err != nil {
        return err
    }
    sch, err := c.Compile("mem://schema.json")
    if err != nil { return err }
    return sch.Validate(obj)
}

// ValidateRecipeMap validates a generic map with a minimal recipe schema.
func ValidateRecipeMap(m map[string]any) error {
    return ValidateJSON(m, recipeSchema)
}

// ValidatePlanMap validates a generic map with a minimal plan schema.
func ValidatePlanMap(m map[string]any) error {
    return ValidateJSON(m, planSchema)
}

// Minimal JSON Schemas for MVP validation
const recipeSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "required":["metadata","lifecycle"],
  "properties":{
    "metadata":{
      "type":"object",
      "required":["name","version"],
      "properties":{
        "name":{"type":"string"},
        "version":{"type":"string"}
      }
    },
    "artifacts":{
      "type":"array",
      "items":{
        "type":"object",
        "required":["uri"],
        "properties":{
          "uri":{"type":"string"},
          "sha256":{"type":"string"},
          "unpack":{"type":"boolean"}
        }
      }
    },
    "lifecycle":{
      "type":"object",
      "required":["run"],
      "properties":{
        "install":{"type":"object"},
        "run":{
          "type":"object",
          "required":["exec"],
          "properties":{
            "exec":{
              "type":"object",
              "required":["command"],
              "properties":{
                "command":{"type":"string"},
                "args":{"type":"array","items":{"type":"string"}}
              }
            },
            "restart_policy":{"type":"string","enum":["never","on-failure","always"]}
          }
        }
      }
    }
  }
}`

const planSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "properties":{
    "components":{
      "type":"array",
      "items":{
        "type":"object",
        "required":["name","recipe"],
        "properties":{
          "name":{"type":"string"},
          "recipe":{"type":"string"}
        }
      }
    }
  }
}`

// Helper to provide io.ReadSeeker from string for jsonschema compiler
func bytesReader(s string) *bytesReaderT { return &bytesReaderT{b: []byte(s)} }

type bytesReaderT struct{ b []byte; i int64 }
func (r *bytesReaderT) Read(p []byte) (int, error) { n := copy(p, r.b[r.i:]); r.i += int64(n); if r.i >= int64(len(r.b)) { return n, io.EOF }; return n, nil }
func (r *bytesReaderT) Seek(off int64, whence int) (int64, error) {
    switch whence { case 0: r.i = off; case 1: r.i += off; case 2: r.i = int64(len(r.b)) + off }
    if r.i < 0 { r.i = 0 }
    if r.i > int64(len(r.b)) { r.i = int64(len(r.b)) }
    return r.i, nil
}
