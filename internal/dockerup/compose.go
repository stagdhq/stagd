package dockerup

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FirstIngressPort is where ingresses are published, one port each in the plan's order, unless the host is shared.
const FirstIngressPort = 18000

// OneShotLabel marks a service that runs to completion: done, not failed, once it exits 0.
const OneShotLabel = "dev.stagd.oneshot"

// healthStartSeconds is how long a service is checked every second while it starts, unless it says.
const healthStartSeconds = 120

// BuiltImage is the local name a service's built image gets, tagged with its project so previews sharing a Docker
// host don't overwrite each other's.
func BuiltImage(project, service string) string {
	return "stagd-build/" + service + ":" + project
}

// Alias is where a service is reached on a shared host's project network.
func Alias(project, service string) string {
	return service + "." + project
}

var conditions = map[string]string{"started": "service_started", "healthy": "service_healthy", "completed": "service_completed_successfully"}

// composeValue is a value for a compose file, where $ starts a variable.
func composeValue(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]struct{}       `yaml:"volumes,omitempty"`
}

type composeService struct {
	Image       string                       `yaml:"image"`
	Networks    map[string]composeNetwork    `yaml:"networks,omitempty"`
	Command     []string                     `yaml:"command,omitempty"`
	Environment map[string]string            `yaml:"environment,omitempty"`
	Ports       []string                     `yaml:"ports,omitempty"`
	Expose      []string                     `yaml:"expose,omitempty"`
	DependsOn   map[string]composeDependency `yaml:"depends_on,omitempty"`
	Labels      map[string]string            `yaml:"labels,omitempty"`
	Volumes     []string                     `yaml:"volumes,omitempty"`
	Healthcheck *composeHealth               `yaml:"healthcheck,omitempty"`
	Restart     string                       `yaml:"restart"`
	Logging     composeLogging               `yaml:"logging"`
}

type composeNetwork struct {
	Aliases []string `yaml:"aliases"`
}

type composeDependency struct {
	Condition string `yaml:"condition"`
}

type composeHealth struct {
	Test          []string `yaml:"test"`
	Interval      string   `yaml:"interval"`
	Retries       int      `yaml:"retries"`
	StartPeriod   string   `yaml:"start_period"`
	StartInterval string   `yaml:"start_interval"`
}

type composeLogging struct {
	Driver  string            `yaml:"driver"`
	Options map[string]string `yaml:"options"`
}

// Compose is the plan as a compose file, run as the project; on a shared host nothing is published, and each service
// is reached on the project's network as <service>.<project>.
func Compose(p *Plan, project string, shared bool) ([]byte, error) {
	file := composeFile{Name: project, Services: map[string]composeService{}}

	for _, s := range p.Services {
		c := composeService{Image: s.Image, Restart: "unless-stopped"}

		if _, built := p.Builds[s.Name]; built {
			c.Image = BuiltImage(project, s.Name)
		}

		if shared {
			c.Networks = map[string]composeNetwork{"default": {Aliases: []string{Alias(project, s.Name)}}}
		}

		for _, arg := range s.Command {
			c.Command = append(c.Command, composeValue(arg))
		}

		// Secrets are filled in by compose from its environment as it starts the containers: never in the file.
		for k, v := range s.Env {
			if c.Environment == nil {
				c.Environment = map[string]string{}
			}

			c.Environment[k] = composeValue(v)
		}

		for k, ref := range s.SecretEnv {
			if c.Environment == nil {
				c.Environment = map[string]string{}
			}

			c.Environment[k] = "${" + ref + "}"
		}

		if !shared {
			for i, in := range p.Ingress {
				if in.Service == s.Name {
					c.Ports = append(c.Ports, fmt.Sprintf("%d:%d", FirstIngressPort+i, in.Port))
				}
			}
		}

		for _, port := range s.Ports {
			c.Expose = append(c.Expose, fmt.Sprint(port))
		}

		for _, dep := range s.DependsOn {
			if c.DependsOn == nil {
				c.DependsOn = map[string]composeDependency{}
			}

			wait := s.WaitFor[dep]

			if wait == "" {
				wait = "started"
			}

			c.DependsOn[dep] = composeDependency{Condition: conditions[wait]}
		}

		if s.OneShot {
			c.Labels = map[string]string{OneShotLabel: "true"}
			c.Restart = "no"
		}

		for _, m := range s.Mounts {
			c.Volumes = append(c.Volumes, m.Volume+":"+m.Path)
		}

		// Checked every second while it starts, so whatever waits for it to be healthy goes on within a second rather
		// than the 5 s Docker would otherwise wait for the first check; every 5 s once it's up.
		if s.Health != nil && len(s.Health.Command) > 0 {
			start := s.Health.StartSeconds

			if start == 0 {
				start = healthStartSeconds
			}

			test := []string{"CMD"}

			for _, arg := range s.Health.Command {
				test = append(test, composeValue(arg))
			}

			c.Healthcheck = &composeHealth{Test: test, Interval: "5s", Retries: 30, StartPeriod: fmt.Sprintf("%ds", start), StartInterval: "1s"}
		}

		// Each service's logs stay on the engine, where Stagd reads them, capped so a chatty one can't fill the disk.
		c.Logging = composeLogging{Driver: "json-file", Options: map[string]string{"max-file": "3", "max-size": "10m"}}

		file.Services[s.Name] = c
	}

	for _, v := range p.Volumes {
		if file.Volumes == nil {
			file.Volumes = map[string]struct{}{}
		}

		file.Volumes[v.Name] = struct{}{}
	}

	var b bytes.Buffer

	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)

	if err := enc.Encode(file); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}
