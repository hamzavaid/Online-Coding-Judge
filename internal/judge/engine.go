// Package judge evaluates immutable submissions through a replaceable sandbox boundary.
package judge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
)

// Job contains authoritative source and test data loaded from PostgreSQL, never from Redis.
type Job struct {
	ID            string
	AttemptID     string
	AttemptNumber int
	DeadLetter    bool
	Language      string
	Source        string
	Problem       problems.Problem
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
	Claim(context.Context, string, string, time.Duration) (Job, error)
	Transition(context.Context, string, string) error
	Finish(context.Context, string, Result) error
	Fail(context.Context, string) error
	Retry(context.Context, string, time.Duration) error
	Renew(context.Context, string, time.Duration) error
}

// Acknowledger removes work only after its durable state has been written.
type Acknowledger interface {
	Ack(context.Context, string) error
	DeadLetter(context.Context, Delivery, string) error
}

// Worker processes one leased submission; independent workers coordinate through the repository.
type Worker struct {
	ID            string
	Lease         time.Duration
	MaxDeliveries int
	Repo          Repository
	Engine        Engine
	Queue         Acknowledger
}

// Delivery describes one at-least-once queue delivery.
type Delivery struct {
	ID           string
	SubmissionID string
	Deliveries   int64
}

// Sentinel errors classify safe duplicate, contention, and shutdown outcomes.
var (
	ErrTerminal     = errors.New("submission already terminal")
	ErrBusy         = errors.New("submission lease active")
	ErrStaleAttempt = errors.New("stale judging attempt")
	ErrQueueClosed  = errors.New("queue closed")
)

// Handle claims, evaluates, persists, and finally acknowledges a queue delivery.
func (w *Worker) Handle(ctx context.Context, delivery Delivery) error {
	job, e := w.Repo.Claim(ctx, delivery.SubmissionID, w.ID, w.Lease)
	if errors.Is(e, ErrTerminal) {
		if job.DeadLetter {
			return w.Queue.DeadLetter(ctx, delivery, "attempts_exhausted")
		}
		return w.Queue.Ack(ctx, delivery.ID)
	}
	if e != nil {
		return e
	}
	evaluateCtx, cancel := context.WithCancel(ctx)
	heartbeat := make(chan error, 1)
	go func() {
		interval := w.Lease / 3
		if interval < 10*time.Millisecond {
			interval = 10 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-evaluateCtx.Done():
				heartbeat <- nil
				return
			case <-ticker.C:
				if renewErr := w.Repo.Renew(evaluateCtx, job.AttemptID, w.Lease); renewErr != nil {
					if evaluateCtx.Err() != nil {
						heartbeat <- nil
					} else {
						heartbeat <- renewErr
						cancel()
					}
					return
				}
			}
		}
	}()
	r, e := w.Engine.Evaluate(evaluateCtx, job, func(state string) error { return w.Repo.Transition(evaluateCtx, job.AttemptID, state) })
	cancel()
	if renewErr := <-heartbeat; renewErr != nil {
		return renewErr
	}
	if e != nil {
		if ctx.Err() != nil {
			return e
		}
		maximum := w.MaxDeliveries
		if maximum < 1 {
			maximum = 3
		}
		if job.AttemptNumber >= maximum {
			if persist := w.Repo.Fail(ctx, job.AttemptID); persist != nil {
				return persist
			}
			if delivery.Deliveries < int64(job.AttemptNumber) {
				delivery.Deliveries = int64(job.AttemptNumber)
			}
			return w.Queue.DeadLetter(ctx, delivery, "attempts_exhausted")
		}
		if persist := w.Repo.Retry(ctx, job.AttemptID, RetryDelay(job.AttemptNumber)); persist != nil {
			return persist
		}
		return w.Queue.Ack(ctx, delivery.ID)
	}
	if e = w.Repo.Finish(ctx, job.AttemptID, r); e != nil {
		return e
	}
	return w.Queue.Ack(ctx, delivery.ID)
}

// RetryDelay returns a bounded exponential delay for an infrastructure failure.
func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 6)
	return delay
}

// RunPool runs a fixed number of indexed consumers and cancels peers on the first error.
func RunPool(ctx context.Context, concurrency int, run func(context.Context, int) error) error {
	if concurrency < 1 || concurrency > 64 {
		return errors.New("invalid worker concurrency")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, concurrency)
	var wg sync.WaitGroup
	for slot := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := run(ctx, slot); e != nil && !errors.Is(e, ErrQueueClosed) {
				errs <- e
				cancel()
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case e := <-errs:
		<-done
		return e
	case <-ctx.Done():
		<-done
		if cause := context.Cause(ctx); cause != context.Canceled {
			return cause
		}
		return nil
	}
}
