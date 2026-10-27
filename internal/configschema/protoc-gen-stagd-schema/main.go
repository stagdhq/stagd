// protoc-gen-stagd-schema writes stagd.yaml's JSON Schema, stagd.schema.json,
// from stagd.config.v1.Config: a buf plugin, run by `task generate`
// (proto/buf.gen.yaml), as it sees the proto's comments.
package main

import (
	"google.golang.org/protobuf/compiler/protogen"

	"github.com/stagdhq/stagd/internal/configschema"
)

func main() {
	protogen.Options{}.Run(func(p *protogen.Plugin) error {
		for _, f := range p.Files {
			if !f.Generate {
				continue
			}

			for _, m := range f.Messages {
				if m.Desc.FullName() != configschema.RootMessage {
					continue
				}

				out, err := configschema.Generate(m)
				if err != nil {
					return err
				}

				if _, err := p.NewGeneratedFile("stagd.schema.json", "").Write(out); err != nil {
					return err
				}
			}
		}

		return nil
	})
}
