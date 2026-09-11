package problems

import "testing"

// TestValidate checks bounded authoring data and supported runtime identifiers.
func TestValidate(t *testing.T) {
	p := Problem{Slug: "sum", Title: "Sum", Statement: "Add integers", TimeLimitMS: 1000, MemoryLimitMB: 128, Status: "published", Languages: []string{"python", "cpp"}, Tests: []TestCase{{Input: "1 2", Expected: "3", Hidden: true}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Problem){func(p *Problem) { p.TimeLimitMS = 0 }, func(p *Problem) { p.MemoryLimitMB = 0 }, func(p *Problem) { p.Languages = []string{"shell"} }, func(p *Problem) { p.Tests = nil }, func(p *Problem) { p.Slug = "../bad" }, func(p *Problem) { p.Status = "other" }} {
		q := p
		mutate(&q)
		if q.Validate() == nil {
			t.Errorf("accepted invalid problem: %+v", q)
		}
	}
}
