package judge

import (
	"bytes"
	"io"
	"sync"
)

// outputBudget shares one byte budget across concurrent stdout and stderr readers.
type outputBudget struct {
	mu        sync.Mutex
	remaining int
	stdout    bytes.Buffer
	exceeded  bool
	cancel    func()
}

// newOutputBudget retains at most limit bytes and cancels the operation on overflow.
func newOutputBudget(limit int, cancel func()) *outputBudget {
	return &outputBudget{remaining: limit, cancel: cancel}
}

// budgetWriter selects whether accepted bytes are retained as stdout or discarded as stderr.
type budgetWriter struct {
	budget *outputBudget
	stdout bool
}

// writer returns a stream adapter; both adapters share the same locked accounting.
func (b *outputBudget) writer(stdout bool) io.Writer { return budgetWriter{b, stdout} }

// Write reports consumed bytes even after overflow so draining cannot allocate or block on a short write.
func (w budgetWriter) Write(p []byte) (int, error) {
	b := w.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(p), b.remaining)
	if w.stdout {
		_, _ = b.stdout.Write(p[:n])
	}
	b.remaining -= n
	if n < len(p) && !b.exceeded {
		b.exceeded = true
		b.cancel()
	}
	return len(p), nil
}

// limited reports whether either stream exceeded the shared budget.
func (b *outputBudget) limited() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.exceeded }

// output returns the retained stdout after command readers finish.
func (b *outputBudget) output() string { b.mu.Lock(); defer b.mu.Unlock(); return b.stdout.String() }
