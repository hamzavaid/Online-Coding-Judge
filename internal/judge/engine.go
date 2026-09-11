// Package judge evaluates immutable submissions through a replaceable sandbox boundary.
package judge

import (
	"context"
	"errors"
	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
	"strings"
)

// Job contains authoritative source and test data loaded from PostgreSQL, never from Redis.
type Job struct {
	ID       string
	Language string
	Source   string
	Problem  problems.Problem
}

// Outcome contains monitor signals and bounded stdout; diagnostic stderr is never public.
type Outcome struct {
	Output        string
	ExitCode      int
	RuntimeMS     int
	MemoryKB      int
	TimedOut      bool
	OOM           bool
	OutputLimited bool
}

// Result is the aggregate durable submission result.
type Result struct {
	Verdict     string
	RuntimeMS   int
	MemoryKB    int
	TestsPassed int
	TestsTotal  int
}

// Sandbox owns one isolated submission workspace and must be closed after evaluation.
type Sandbox interface {
	Compile(context.Context) (Outcome, error)
	Run(context.Context, string) (Outcome, error)
	Close() error
}

// Factory creates an execution boundary with no access to expected outputs.
type Factory interface {
	Open(context.Context, Job) (Sandbox, error)
}

// Engine applies compile-first and first-failing-test semantics.
type Engine struct{ Factory Factory }

// Compare treats output as exact whitespace-separated tokens; it does not round numbers.
func Compare(actual, expected string) bool {
	a, b := strings.Fields(actual), strings.Fields(expected)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Evaluate persists each lifecycle transition through transition and always tears down the sandbox.
func (e *Engine) Evaluate(ctx context.Context, job Job, transition func(string) error) (result Result, err error) {
	result.TestsTotal = len(job.Problem.Tests)
	if result.TestsTotal == 0 {
		return result, errors.New("no tests")
	}
	// Keep hidden material out of the sandbox factory, even when implementations change.
	execution := job
	execution.Problem.Tests = nil
	s, err := e.Factory.Open(ctx, execution)
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := s.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if err = transition("COMPILING"); err != nil {
		return result, err
	}
	compiled, err := s.Compile(ctx)
	if err != nil {
		return result, err
	}
	if compiled.ExitCode != 0 || compiled.TimedOut || compiled.OOM || compiled.OutputLimited {
		result.Verdict = "COMPILATION_ERROR"
		return result, nil
	}
	if err = transition("RUNNING"); err != nil {
		return result, err
	}
	result.Verdict = "ACCEPTED"
	for _, test := range job.Problem.Tests {
		out, e := s.Run(ctx, test.Input)
		if e != nil {
			return result, e
		}
		result.RuntimeMS = max(result.RuntimeMS, out.RuntimeMS)
		result.MemoryKB = max(result.MemoryKB, out.MemoryKB)
		switch {
		case out.OOM:
			result.Verdict = "MEMORY_LIMIT_EXCEEDED"
		case out.OutputLimited:
			result.Verdict = "OUTPUT_LIMIT_EXCEEDED"
		case out.TimedOut:
			result.Verdict = "TIME_LIMIT_EXCEEDED"
		case out.ExitCode != 0:
			result.Verdict = "RUNTIME_ERROR"
		case !Compare(out.Output, test.Expected):
			result.Verdict = "WRONG_ANSWER"
		}
		if result.Verdict != "ACCEPTED" {
			break
		}
		result.TestsPassed++
	}
	return result, nil
}

// Repository provides atomic lifecycle writes; the queue never owns authoritative state.
type Repository interface {
	Claim(context.Context, string) (Job, error)
	Transition(context.Context, string, string) error
	Finish(context.Context, string, Result) error
	Fail(context.Context, string) error
}

// Acknowledger removes work only after its durable state has been written.
type Acknowledger interface {
	Ack(context.Context, string) error
}

// Worker processes one submission at a time; horizontal coordination is a later milestone.
type Worker struct {
	Repo   Repository
	Engine Engine
	Queue  Acknowledger
}

// Handle claims, evaluates, persists, and finally acknowledges a queue delivery.
func (w *Worker) Handle(ctx context.Context, id, message string) error {
	job, e := w.Repo.Claim(ctx, id)
	if e != nil {
		return e
	}
	r, e := w.Engine.Evaluate(ctx, job, func(state string) error { return w.Repo.Transition(ctx, id, state) })
	if e != nil {
		if ctx.Err() != nil {
			return e
		}
		if persist := w.Repo.Fail(ctx, id); persist != nil {
			return persist
		}
		return w.Queue.Ack(ctx, message)
	}
	if e = w.Repo.Finish(ctx, id, r); e != nil {
		return e
	}
	return w.Queue.Ack(ctx, message)
}
