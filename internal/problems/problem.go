// Package problems defines bounded problem authoring and public projections.
package problems

import (
	"errors"
	"regexp"
)

// TestCase contains one bounded input and its expected output; hidden cases stay server-side.
type TestCase struct {
	Input    string `json:"input"`
	Expected string `json:"expected"`
	Hidden   bool   `json:"hidden"`
}

// Problem is a versioned-by-submission problem definition owned by trusted staff.
type Problem struct {
	ID            string     `json:"id,omitempty"`
	Slug          string     `json:"slug"`
	Title         string     `json:"title"`
	Statement     string     `json:"statement"`
	Difficulty    string     `json:"difficulty"`
	TimeLimitMS   int        `json:"time_limit_ms"`
	MemoryLimitMB int        `json:"memory_limit_mb"`
	Status        string     `json:"status"`
	Languages     []string   `json:"languages"`
	Tests         []TestCase `json:"tests"`
}

// Validate rejects unsupported runtimes and data exceeding the initial platform budgets.
func (p Problem) Validate() error {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`).MatchString(p.Slug) || len(p.Title) < 1 || len(p.Title) > 200 || len(p.Statement) < 1 || len(p.Statement) > 65536 || p.TimeLimitMS < 100 || p.TimeLimitMS > 10000 || p.MemoryLimitMB < 32 || p.MemoryLimitMB > 512 || (p.Status != "draft" && p.Status != "published" && p.Status != "disabled") || len(p.Tests) < 1 || len(p.Tests) > 100 || len(p.Languages) < 1 || len(p.Languages) > 2 {
		return errors.New("invalid problem")
	}
	seen := map[string]bool{}
	for _, l := range p.Languages {
		if (l != "python" && l != "cpp") || seen[l] {
			return errors.New("invalid language")
		}
		seen[l] = true
	}
	total := 0
	for _, c := range p.Tests {
		if len(c.Input) > 65536 || len(c.Expected) > 65536 {
			return errors.New("test too large")
		}
		total += len(c.Input) + len(c.Expected)
	}
	if total > 1048576 {
		return errors.New("test set too large")
	}
	return nil
}

// Public returns a copy containing only visible sample cases.
func (p Problem) Public() Problem {
	p.Tests = append([]TestCase{}, p.Tests...)
	visible := []TestCase{}
	for _, c := range p.Tests {
		if !c.Hidden {
			visible = append(visible, c)
		}
	}
	p.Tests = visible
	return p
}
