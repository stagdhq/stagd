// Package configyaml writes a stagd.config.v1.Config back out as a
// stagd.yaml: the plain form, in the proto's field order, durations as
// stagd.yaml writes them (20m, 1h30m). What Stagd reads it as reads back the
// same, so it can be pasted into a repository.
package configyaml

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"

	configv1 "github.com/stagdhq/stagd/internal/gen/stagd/config/v1"
)

// Schema is where editors find stagd.yaml's JSON Schema.
const Schema = "https://stagd.dev/schema/stagd.schema.json"

// Marshal is c as a stagd.yaml, starting with the comment that points
// editors at its schema.
func Marshal(c *configv1.Config) ([]byte, error) {
	doc := message(c.ProtoReflect())
	doc.HeadComment = "yaml-language-server: $schema=" + Schema

	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(doc); err != nil {
		return nil, err
	}

	if err := enc.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// message is m's set fields, in the order the proto declares them.
func message(m protoreflect.Message) *yaml.Node {
	if d, ok := m.Interface().(*durationpb.Duration); ok {
		return str(Duration(d.AsDuration()))
	}

	out := &yaml.Node{Kind: yaml.MappingNode}
	fields := m.Descriptor().Fields()

	for i := range fields.Len() {
		f := fields.Get(i)

		if !m.Has(f) {
			continue
		}

		out.Content = append(out.Content, str(string(f.Name())), value(f, m.Get(f)))
	}

	return compact(out)
}

func value(f protoreflect.FieldDescriptor, v protoreflect.Value) *yaml.Node {
	switch {
	case f.IsMap():
		out := &yaml.Node{Kind: yaml.MappingNode}
		entries := map[string]protoreflect.Value{}

		v.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			entries[k.String()] = v

			return true
		})

		keys := make([]string, 0, len(entries))
		for k := range entries {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		for _, k := range keys {
			out.Content = append(out.Content, str(k), single(f.MapValue(), entries[k]))
		}

		return compact(out)

	case f.IsList():
		out := &yaml.Node{Kind: yaml.SequenceNode}
		list := v.List()

		for i := range list.Len() {
			item := single(f, list.Get(i))

			// - click: Sign in, not - {click: Sign in}.
			if item.Kind == yaml.MappingNode && len(item.Content) > 0 {
				item.Style = 0
			}

			out.Content = append(out.Content, item)
		}

		return compact(out)
	}

	return single(f, v)
}

// single is one value of f's kind: a message, or a scalar.
func single(f protoreflect.FieldDescriptor, v protoreflect.Value) *yaml.Node {
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return message(v.Message())

	case protoreflect.StringKind:
		return str(v.String())

	case protoreflect.BoolKind:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v.Bool())}

	case protoreflect.EnumKind:
		return str(string(f.Enum().Values().ByNumber(v.Enum()).Name()))

	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(v.Float(), 'g', -1, 64)}
	}

	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(v.Interface())}
}

// str is a string as a string, quoted when YAML would read it as something
// else (WORKERS: "4").
func str(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}

	// YAML 1.1's booleans, which yaml.v3 leaves bare: older readers take
	// on and yes for true.
	if yaml11Bools[strings.ToLower(s)] {
		n.Style = yaml.DoubleQuotedStyle
	}

	return n
}

var yaml11Bools = map[string]bool{"y": true, "yes": true, "n": true, "no": true, "on": true, "off": true}

// compact writes short mappings and lists of plain values on one line, as
// stagd.yaml's docs do: { service: web, port: 3000 }, [smtp, ses], {}.
func compact(n *yaml.Node) *yaml.Node {
	if len(n.Content) == 0 {
		n.Style = yaml.FlowStyle

		return n
	}

	width := 0

	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode || strings.Contains(c.Value, "\n") {
			return n
		}

		width += len(c.Value) + 2
	}

	if width <= 60 {
		n.Style = yaml.FlowStyle
	}

	return n
}

// Duration is d as stagd.yaml writes durations: 20m, 1h30m, 36h, 45s.
func Duration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}

	s := d.String()

	// 1h30m0s → 1h30m; 2h0m0s → 2h.
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}

	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}

	return s
}
