package configschema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"

	configv1 "github.com/stagdhq/stagd/internal/gen/stagd/config/v1"
)

// stagd.yaml's shared cases, which every reader runs.
const testdata = "../../proto/stagd/config/v1/testdata"

type expected struct {
	Config   json.RawMessage `json:"config"`
	Problems []string        `json:"problems"`
}

func cases(t *testing.T) map[string]expected {
	t.Helper()

	names, err := filepath.Glob(filepath.Join(testdata, "*.yaml"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no cases in %s: %v", testdata, err)
	}

	out := map[string]expected{}

	for _, name := range names {
		raw, err := os.ReadFile(strings.TrimSuffix(name, ".yaml") + ".json")
		if err != nil {
			t.Fatalf("%s has no .json: %v", name, err)
		}

		var e expected
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if (e.Config == nil) == (len(e.Problems) == 0) {
			t.Fatalf("%s: wants config or problems, one of them", name)
		}

		out[name] = e
	}

	return out
}

// Each case's config is a Config, every field one the proto has, within
// its rules.
func TestCasesAreConfigs(t *testing.T) {
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}

	for name, e := range cases(t) {
		if e.Config == nil {
			continue
		}

		var c configv1.Config
		if err := protojson.Unmarshal(e.Config, &c); err != nil {
			t.Errorf("%s: not a Config: %v", name, err)

			continue
		}

		if err := validator.Validate(&c); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func schema(t *testing.T) *jsonschema.Schema {
	t.Helper()

	f, err := os.Open("../../schema/stagd.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}

	c := jsonschema.NewCompiler()

	if err := c.AddResource(ID, doc); err != nil {
		t.Fatal(err)
	}

	s, err := c.Compile(ID)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

// asJSON is a YAML document as JSON values: an empty file is an empty map.
func asJSON(t *testing.T, raw []byte) any {
	t.Helper()

	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	if doc == nil {
		return map[string]any{}
	}

	// Through JSON, for the validator's own types.
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	out, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// The schema takes every file that reads, as it's written (shorthands too),
// and in its plain form, as each case's config.
func TestSchemaTakesWhatReads(t *testing.T) {
	s := schema(t)

	for name, e := range cases(t) {
		if e.Config == nil {
			continue
		}

		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}

		if err := s.Validate(asJSON(t, raw)); err != nil {
			t.Errorf("%s as written: %v", name, err)
		}

		if err := s.Validate(asJSON(t, e.Config)); err != nil {
			t.Errorf("%s's config: %v", name, err)
		}
	}
}

// And turns away what can't be set in the repository, or isn't a setting.
func TestSchemaTurnsAwayUnknownSettings(t *testing.T) {
	s := schema(t)

	for _, doc := range []string{
		"access: public",
		"servces: { mail: {} }",
		"ingress: { web: { service: web, port: 3000, public: true } }",
		"previews: sometimes",
		"commands: { handled: archive }",
		"build: { web: { dockerfile: Dockerfile } }",
		"registries: { ghcr.io: { username: bot, password: hunter2 } }",
		"version: 2",
	} {
		if err := s.Validate(asJSON(t, []byte(doc))); err == nil {
			t.Errorf("%q: the schema took it", doc)
		}
	}
}
