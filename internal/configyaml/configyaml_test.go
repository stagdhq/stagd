package configyaml

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"

	configv1 "github.com/stagdhq/stagd/internal/gen/stagd/config/v1"
)

// stagd.yaml's shared cases, which every reader runs.
const testdata = "../../proto/stagd/config/v1/testdata"

// configs are the cases that read, as their Configs.
func configs(t *testing.T) map[string]*configv1.Config {
	t.Helper()

	names, err := filepath.Glob(filepath.Join(testdata, "*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no cases in %s: %v", testdata, err)
	}

	out := map[string]*configv1.Config{}

	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}

		var e struct {
			Config json.RawMessage `json:"config"`
		}

		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if e.Config == nil {
			continue
		}

		c := &configv1.Config{}

		if err := protojson.Unmarshal(e.Config, c); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		out[filepath.Base(name)] = c
	}

	return out
}

// readBack reads a stagd.yaml in its plain form as a Config: durations as
// stagd.yaml writes them, the rest as protojson has it.
func readBack(t *testing.T, raw []byte) *configv1.Config {
	t.Helper()

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, raw)
	}

	durations(t, (&configv1.Config{}).ProtoReflect().Descriptor(), doc)

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	c := &configv1.Config{}

	if err := protojson.Unmarshal(b, c); err != nil {
		t.Fatalf("doesn't read back: %v\n%s", err, raw)
	}

	return c
}

// durations turns stagd.yaml's durations in doc (a d message) into protojson's.
func durations(t *testing.T, d protoreflect.MessageDescriptor, doc map[string]any) {
	t.Helper()

	for key, v := range doc {
		f := d.Fields().ByName(protoreflect.Name(key))
		if f == nil || f.Message() == nil {
			continue
		}

		if f.IsMap() {
			if f.MapValue().Message() == nil {
				continue
			}

			for _, e := range v.(map[string]any) {
				durations(t, f.MapValue().Message(), e.(map[string]any))
			}

			continue
		}

		if f.Message().FullName() == "google.protobuf.Duration" {
			parsed, err := time.ParseDuration(v.(string))
			if err != nil {
				t.Fatalf("%s: %v", key, err)
			}

			// protojson writes a Duration as a JSON string: "1200s".
			doc[key] = strings.Trim(protojson.Format(durationpb.New(parsed)), `"`)

			continue
		}

		if f.IsList() {
			for _, e := range v.([]any) {
				durations(t, f.Message(), e.(map[string]any))
			}

			continue
		}

		durations(t, f.Message(), v.(map[string]any))
	}
}

// Every case that reads prints as YAML that reads back to the same Config.
func TestReadsBackTheSame(t *testing.T) {
	for name, c := range configs(t) {
		out, err := Marshal(c)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if got := readBack(t, out); !proto.Equal(got, c) {
			t.Errorf("%s reads back differently:\n%s", name, out)
		}
	}
}

// And the schema editors use takes it.
func TestSchemaTakesIt(t *testing.T) {
	f, err := os.Open("../../schema/stagd.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}

	compiler := jsonschema.NewCompiler()

	if err := compiler.AddResource(Schema, doc); err != nil {
		t.Fatal(err)
	}

	schema, err := compiler.Compile(Schema)
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range configs(t) {
		out, err := Marshal(c)
		if err != nil {
			t.Fatal(err)
		}

		var parsed any
		if err := yaml.Unmarshal(out, &parsed); err != nil {
			t.Fatal(err)
		}

		b, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}

		value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
		if err != nil {
			t.Fatal(err)
		}

		if err := schema.Validate(value); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
}

func TestFullReadsAsWritten(t *testing.T) {
	out, err := Marshal(configs(t)["full.json"])
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"# yaml-language-server: $schema=" + Schema + "\n",
		"version: 1\n",
		"web: {service: web, port: 3000, primary: true}",
		`WORKERS: "4"`,
		"wait: 20m",
		"sleep_after: 2h",
		"expire_after: 36h",
		"mail: {}",
		"timeout: 45m",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("misses %q:\n%s", want, out)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Minute:            "20m",
		90 * time.Minute:            "1h30m",
		36 * time.Hour:              "36h",
		45 * time.Second:            "45s",
		time.Hour + 30*time.Second:  "1h0m30s",
		1500 * time.Millisecond:     "1.5s",
		2*time.Hour + 5*time.Minute: "2h5m",
	} {
		if got := Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

// A list's mappings are written as blocks, one setting a line.
func TestStepsAreBlocks(t *testing.T) {
	out, err := Marshal(configs(t)["testers.json"])
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"- click: Send code\n", "- wait_for_mail:\n", "within: 1m\n"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("misses %q:\n%s", want, out)
		}
	}
}

// Words older YAML readers take for booleans stay strings.
func TestYAML11BooleansAreQuoted(t *testing.T) {
	out, err := Marshal(&configv1.Config{Version: 1, Env: map[string]string{"A": "on", "B": "No", "C": "true", "D": "only"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{`A: "on"`, `B: "No"`, `C: "true"`, "D: only"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("misses %q:\n%s", want, out)
		}
	}
}
