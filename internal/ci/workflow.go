// Package ci owns workflow definition parsing and scheduling for the CI
// system. Workflow files live at .novaforge/workflow.yaml in the repository
// under test.
package ci

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// Job is one job in a Workflow: either a shell command (Run) or an agent
// role (Agent), never both.
type Job struct {
	Run     string            `yaml:"run"`
	Agent   string            `yaml:"agent"`
	Needs   []string          `yaml:"needs"`
	Image   string            `yaml:"image"`
	Env     map[string]string `yaml:"env"`
	Secrets []string          `yaml:"secrets"`
	// Environment scopes the secrets the job may be brokered: staging (the
	// default) or production. It is a request, not a permission — the
	// broker decides whether a production job gets production material.
	Environment string `yaml:"environment"`
	// Artifacts are paths, relative to the checkout, to keep after the job.
	// They are declared rather than inferred: collecting everything a job
	// wrote would ship its whole working tree, including its credentials.
	Artifacts []string `yaml:"artifacts"`
}

// Workflow is a parsed .novaforge/workflow.yaml document.
type Workflow struct {
	Name string         `yaml:"name"`
	Jobs map[string]Job `yaml:"jobs"`
}

// secretNameRe matches secrets.ValidName; it is repeated rather than imported
// so the workflow parser does not pull the broker's database code in.
var secretNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

// ParseWorkflow parses data as a workflow document, rejecting a job that
// declares neither or both of run and agent, and a needs entry naming an
// unknown job.
func ParseWorkflow(data []byte) (Workflow, error) {
	var w Workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		return Workflow{}, fmt.Errorf("parse workflow: %w", err)
	}

	for name, job := range w.Jobs {
		hasRun := job.Run != ""
		hasAgent := job.Agent != ""
		if hasRun == hasAgent {
			return Workflow{}, fmt.Errorf("job %q must declare exactly one of run or agent", name)
		}
	}
	for name, job := range w.Jobs {
		switch job.Environment {
		case "", "staging", "production":
		default:
			return Workflow{}, fmt.Errorf("job %q: environment %q must be staging or production", name, job.Environment)
		}
		for _, secret := range job.Secrets {
			// A secret reaches the job as an environment variable of the same
			// name, so a name that cannot be one is refused here, where the
			// person who wrote it will see why.
			if !secretNameRe.MatchString(secret) {
				return Workflow{}, fmt.Errorf("job %q: secret %q must be upper-case letters, digits and underscores", name, secret)
			}
		}
		if len(job.Secrets) > 0 && job.Agent != "" {
			return Workflow{}, fmt.Errorf("job %q: an agent job is not brokered secrets", name)
		}
	}
	for name, job := range w.Jobs {
		for _, need := range job.Needs {
			if _, ok := w.Jobs[need]; !ok {
				return Workflow{}, fmt.Errorf("job %q needs unknown job %q", name, need)
			}
		}
	}

	return w, nil
}

// TopoSort orders w's jobs by their Needs edges using Kahn's algorithm,
// breaking ties by name so the order is deterministic across runs, and
// returning an error when the graph contains a cycle.
func TopoSort(w Workflow) ([]string, error) {
	inDegree := make(map[string]int, len(w.Jobs))
	dependents := make(map[string][]string, len(w.Jobs))
	for name := range w.Jobs {
		inDegree[name] = 0
	}
	for name, job := range w.Jobs {
		for _, need := range job.Needs {
			inDegree[name]++
			dependents[need] = append(dependents[need], name)
		}
	}

	var ready []string
	for name, deg := range inDegree {
		if deg == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)

	var order []string
	for len(ready) > 0 {
		sort.Strings(ready)
		next := ready[0]
		ready = ready[1:]
		order = append(order, next)

		for _, dep := range dependents[next] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				ready = append(ready, dep)
			}
		}
	}

	if len(order) != len(w.Jobs) {
		return nil, errors.New("workflow contains a dependency cycle")
	}
	return order, nil
}
