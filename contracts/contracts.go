package contracts

import (
	"embed"
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed core/*.json
var files embed.FS

func EventSchema() (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	for _, n := range []string{"entity_ref", "provider_manifest", "core_event"} {
		b, e := files.ReadFile("core/" + n + ".schema.json")
		if e != nil {
			return nil, e
		}
		var d any
		if e = json.Unmarshal(b, &d); e != nil {
			return nil, e
		}
		if e = c.AddResource("https://schemas.example.invalid/ops-observability/core/v1/"+n+".schema.json", d); e != nil {
			return nil, e
		}
	}
	return c.Compile("https://schemas.example.invalid/ops-observability/core/v1/core_event.schema.json")
}
