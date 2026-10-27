// Package configschema makes stagd.yaml's JSON Schema, for editors, from
// stagd.config.v1.Config: its fields, their comments and their protovalidate
// rules. stagd.yaml has the messages' own shape, so the schema is theirs,
// plus the shorthands a file may use (config.proto's Config).
//
// protoc-gen-stagd-schema runs it in `task generate`, into
// schema/stagd.schema.json; editors find it with a first line of
//
//	# yaml-language-server: $schema=https://stagd.dev/schema/stagd.schema.json
package configschema

import (
	"encoding/json"
	"fmt"
	"strings"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ID is where the schema is published.
const ID = "https://stagd.dev/schema/stagd.schema.json"

// RootMessage is the message a stagd.yaml is.
const RootMessage = "stagd.config.v1.Config"

// A Go or Kubernetes style duration as stagd.yaml takes it, e.g. 20m, 1h30m
// or 36h; whole days too (2d), and protojson's own (1200s).
const durationPattern = `^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h|d))+$`

// Shorthands: what a message may also be written as, besides itself.
var shorthands = map[protoreflect.FullName]func(*protogen.Message) []any{
	// previews: always, for { when: always }.
	"stagd.config.v1.Previews": func(m *protogen.Message) []any {
		for _, f := range m.Fields {
			if f.Desc.Name() == "when" {
				return []any{field(f)}
			}
		}

		return nil
	},

	// mail: true or mail: (empty) turn it on; false leaves it out.
	"stagd.config.v1.Mail": onOff,
	"stagd.config.v1.OTel": onOff,
}

func onOff(*protogen.Message) []any {
	return []any{map[string]any{"type": []string{"boolean", "null"}}}
}

// Generate is the schema for root and the messages it uses, as JSON.
func Generate(root *protogen.Message) ([]byte, error) {
	defs := map[string]any{}
	collect(root, defs)

	schema := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         ID,
		"title":       "stagd.yaml",
		"description": comment(root.Comments.Leading),
		"$ref":        ref(root),
		"$defs":       defs,
	}

	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(out, '\n'), nil
}

func ref(m *protogen.Message) string {
	return "#/$defs/" + string(m.Desc.Name())
}

// collect adds m and every message its fields use to defs.
func collect(m *protogen.Message, defs map[string]any) {
	name := string(m.Desc.Name())

	if _, done := defs[name]; done || wellKnown(m.Desc.FullName()) {
		return
	}

	// Taken before its fields are, so a message that uses itself stops here.
	defs[name] = nil
	defs[name] = message(m)

	for _, f := range m.Fields {
		if f.Message == nil {
			continue
		}

		if f.Desc.IsMap() {
			if v := f.Message.Fields[1]; v.Message != nil {
				collect(v.Message, defs)
			}

			continue
		}

		collect(f.Message, defs)
	}
}

func wellKnown(name protoreflect.FullName) bool {
	return strings.HasPrefix(string(name), "google.protobuf.")
}

func message(m *protogen.Message) map[string]any {
	properties := map[string]any{}

	for _, f := range m.Fields {
		properties[string(f.Desc.Name())] = field(f)
	}

	s := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}

	if d := comment(m.Comments.Leading); d != "" {
		s["description"] = d
	}

	// One of a oneof's fields, when it's required or the message is nothing else.
	for _, o := range m.Oneofs {
		if o.Desc.IsSynthetic() {
			continue
		}

		var each []any

		for _, f := range o.Fields {
			each = append(each, map[string]any{"required": []string{string(f.Desc.Name())}})
		}

		if required, _ := proto.GetExtension(o.Desc.Options(), validate.E_Oneof).(*validate.OneofRules); required.GetRequired() || len(o.Fields) == len(m.Fields) {
			s["oneOf"] = each
		}
	}

	if short := shorthands[m.Desc.FullName()]; short != nil {
		return map[string]any{"anyOf": append([]any{s}, short(m)...)}
	}

	return s
}

// field is a field's schema: its type, with its rules and comment.
func field(f *protogen.Field) map[string]any {
	rules, _ := proto.GetExtension(f.Desc.Options(), validate.E_Field).(*validate.FieldRules)

	var s map[string]any

	switch {
	case f.Desc.IsMap():
		s = map[string]any{"type": "object", "additionalProperties": mapValue(f.Message.Fields[1])}

		if keys := stringRules(rules.GetMap().GetKeys().GetString()); len(keys) > 0 {
			keys["type"] = "string"
			s["propertyNames"] = keys
		}

	case f.Desc.IsList():
		s = map[string]any{"type": "array", "items": scalar(f, rules.GetRepeated().GetItems())}

		if r := rules.GetRepeated(); r.HasMinItems() {
			s["minItems"] = r.GetMinItems()
		}

		if r := rules.GetRepeated(); r.HasMaxItems() {
			s["maxItems"] = r.GetMaxItems()
		}

	default:
		s = scalar(f, rules)
	}

	if d := comment(f.Comments.Leading); d != "" {
		s["description"] = d
	}

	return s
}

// mapValue is a map's values: a scalar written in YAML, a number say, reads as text.
func mapValue(v *protogen.Field) any {
	if v.Message != nil {
		return map[string]any{"$ref": ref(v.Message)}
	}

	if v.Desc.Kind() == protoreflect.StringKind {
		return map[string]any{"type": []string{"string", "number", "boolean"}}
	}

	return scalar(v, nil)
}

func scalar(f *protogen.Field, rules *validate.FieldRules) map[string]any {
	switch f.Desc.Kind() {
	case protoreflect.StringKind:
		s := stringRules(rules.GetString())
		s["type"] = "string"

		return s

	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		s := map[string]any{"type": "integer"}

		if r := rules.GetInt32(); r != nil {
			if r.HasConst() {
				s["const"] = r.GetConst()
			}

			if r.HasGte() {
				s["minimum"] = r.GetGte()
			}

			if r.HasLte() {
				s["maximum"] = r.GetLte()
			}
		}

		return s

	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}

	case protoreflect.MessageKind:
		if f.Message.Desc.FullName() == "google.protobuf.Duration" {
			return map[string]any{"type": "string", "pattern": durationPattern}
		}

		return map[string]any{"$ref": ref(f.Message)}
	}

	panic(fmt.Sprintf("configschema: %s is a %s, which stagd.yaml has no use for yet", f.Desc.FullName(), f.Desc.Kind()))
}

func stringRules(r *validate.StringRules) map[string]any {
	s := map[string]any{}

	if r == nil {
		return s
	}

	if len(r.GetIn()) > 0 {
		s["enum"] = r.GetIn()
	}

	if r.HasPattern() {
		s["pattern"] = r.GetPattern()
	}

	if r.HasMinLen() {
		s["minLength"] = r.GetMinLen()
	}

	if r.HasMaxLen() {
		s["maxLength"] = r.GetMaxLen()
	}

	return s
}

// comment is a proto comment as a description: a paragraph's lines joined
// into one, the indented ones (examples) kept as they are.
func comment(c protogen.Comments) string {
	var paragraphs []string

	for _, p := range strings.Split(strings.TrimSpace(string(c)), "\n\n") {
		lines := strings.Split(p, "\n")
		indented := false

		for i, l := range lines {
			lines[i] = strings.TrimPrefix(l, " ")
			indented = indented || strings.HasPrefix(lines[i], "  ")
		}

		if indented {
			paragraphs = append(paragraphs, strings.Join(lines, "\n"))
		} else {
			paragraphs = append(paragraphs, strings.Join(lines, " "))
		}
	}

	return strings.Join(paragraphs, "\n\n")
}
