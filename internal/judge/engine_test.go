package judge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
)

// TestCompare fixes token comparison semantics, including Unicode whitespace and exact numeric tokens.
func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1  2\n", "1\t2", true}, {"π\n", "π", true}, {"1.0", "1", false}, {"a", "b", false}, {"", " \n", true}} {
		if Compare(c.a, c.b) != c.want {
			t.Errorf("%q vs %q", c.a, c.b)
		}
	}
}

type fakeSandbox struct {
	compile Outcome
	runs    []Outcome
	inputs  []string
	closed  bool
}

func (s *fakeSandbox) Compile(context.Context) (Outcome, error) { return s.compile, nil }
func (s *fakeSandbox) Run(_ context.Context, input string) (Outcome, error) {
	s.inputs = append(s.inputs, input)
	r := s.runs[0]
	s.runs = s.runs[1:]
	return r, nil
}
func (s *fakeSandbox) Close() error { s.closed = true; return nil }

type fakeFactory struct{ s *fakeSandbox }

func (f fakeFactory) Open(context.Context, Job) (Sandbox, error) { return f.s, nil }

// TestEngine verifies compile-first ordering, first-failure aggregation, and cleanup.
func TestEngine(t *testing.T) {
	for _, c := range []struct {
		name, lang string
		compile    Outcome
		runs       []Outcome
		want       string
		passed     int
	}{
		{"accepted", "python", Outcome{}, []Outcome{{Output: "3", RuntimeMS: 2}, {Output: "7", RuntimeMS: 5}}, "ACCEPTED", 2},
		{"wrong", "python", Outcome{}, []Outcome{{Output: "0"}}, "WRONG_ANSWER", 0},
		{"compile", "cpp", Outcome{ExitCode: 1}, nil, "COMPILATION_ERROR", 0},
		{"runtime", "python", Outcome{}, []Outcome{{ExitCode: 1}}, "RUNTIME_ERROR", 0},
		{"timeout", "python", Outcome{}, []Outcome{{TimedOut: true, ExitCode: 137}}, "TIME_LIMIT_EXCEEDED", 0},
		{"oom", "python", Outcome{}, []Outcome{{OOM: true, ExitCode: 137}}, "MEMORY_LIMIT_EXCEEDED", 0},
		{"output", "python", Outcome{}, []Outcome{{OutputLimited: true, ExitCode: 137}}, "OUTPUT_LIMIT_EXCEEDED", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &fakeSandbox{compile: c.compile, runs: c.runs}
			e := Engine{Factory: fakeFactory{s}}
			states := []string{}
			result, err := e.Evaluate(context.Background(), Job{Language: c.lang, Problem: problems.Problem{Tests: []problems.TestCase{{Input: "1 2", Expected: "3"}, {Input: "3 4", Expected: "7", Hidden: true}}}}, func(state string) error { states = append(states, state); return nil })
			if err != nil || result.Verdict != c.want || result.TestsPassed != c.passed || !s.closed {
				t.Fatalf("%+v %v closed=%v", result, err, s.closed)
			}
			if c.lang == "cpp" && states[0] != "COMPILING" {
				t.Fatal(states)
			}
			if c.name == "accepted" && result.RuntimeMS != 5 {
				t.Fatal(result)
			}
		})
	}
}

type fakeRepo struct {
	events *[]string
	fail   bool
}

func (r fakeRepo) Claim(context.Context, string, string, time.Duration) (Job, error) {
	*r.events = append(*r.events, "claim")
	return Job{AttemptID: "attempt", AttemptNumber: 1, Language: "python", Problem: problems.Problem{Tests: []problems.TestCase{{Expected: "3"}}}}, nil
}
func (r fakeRepo) Transition(_ context.Context, _, state string) error {
	*r.events = append(*r.events, state)
	return nil
}
func (r fakeRepo) Finish(context.Context, string, Result) error {
	*r.events = append(*r.events, "persist")
	if r.fail {
		return errors.New("database unavailable")
	}
	return nil
}
func (r fakeRepo) Fail(context.Context, string) error                 { return nil }
func (r fakeRepo) Retry(context.Context, string, time.Duration) error { return nil }
func (r fakeRepo) Renew(context.Context, string, time.Duration) error { return nil }

type fakeAck struct{ events *[]string }

func (q fakeAck) Ack(context.Context, string) error                  { *q.events = append(*q.events, "ack"); return nil }
func (q fakeAck) DeadLetter(context.Context, Delivery, string) error { return nil }

// TestWorkerCommitBeforeAck makes persistence failures leave queue messages unacknowledged.
func TestWorkerCommitBeforeAck(t *testing.T) {
	for _, fail := range []bool{false, true} {
		events := []string{}
		w := Worker{ID: "worker", Lease: time.Minute, Repo: fakeRepo{&events, fail}, Engine: Engine{Factory: fakeFactory{&fakeSandbox{runs: []Outcome{{Output: "3"}}}}}, Queue: fakeAck{&events}}
		err := w.Handle(context.Background(), Delivery{ID: "msg", SubmissionID: "sub"})
		want := []string{"claim", "COMPILING", "RUNNING", "persist"}
		if !fail {
			want = append(want, "ack")
		}
		if !reflect.DeepEqual(events, want) || (err != nil) != fail {
			t.Fatalf("%v %v", events, err)
		}
	}
}
