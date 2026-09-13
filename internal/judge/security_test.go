package judge

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSandboxPolicy requires every execution boundary to carry the mandatory controls.
func TestSandboxPolicy(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	args, e := sandboxArgs(image, 128)
	if e != nil {
		t.Fatal(e)
	}
	joined := strings.Join(args, " ")
	for _, flag := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--user=65534:65534", "--security-opt=no-new-privileges", "--pids-limit=64", "--cpus=1", "--memory=128m", "--memory-swap=128m", "--tmpfs=/work:rw,exec,", "--tmpfs=/tmp:rw,noexec,", "--ulimit=fsize=16777216:16777216", "--log-driver=none"} {
		if !strings.Contains(joined, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	if strings.Contains(joined, "--privileged") || strings.Contains(joined, "--volume") || strings.Contains(joined, "--mount") {
		t.Fatal("host exposure")
	}
	for _, memory := range []int{0, 16, 1024} {
		if _, e = sandboxArgs(image, memory); e == nil {
			t.Fatal("unbounded memory")
		}
	}
	if _, e = sandboxArgs("python:latest", 128); e == nil {
		t.Fatal("mutable image")
	}
}

// TestOutputBudget bounds combined stdout/stderr under concurrent flooding and cancels once.
func TestOutputBudget(t *testing.T) {
	var calls atomic.Int32
	b := newOutputBudget(64, func() { calls.Add(1) })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := b.writer(i%2 == 0)
			for n := 0; n < 10; n++ {
				p := []byte(strings.Repeat("x", 40))
				if size, e := w.Write(p); e != nil || size != len(p) {
					t.Errorf("write %d %v", size, e)
				}
			}
		}(i)
	}
	wg.Wait()
	if !b.limited() || len(b.output()) > 64 || calls.Load() != 1 {
		t.Fatalf("limited=%v retained=%d cancels=%d", b.limited(), len(b.output()), calls.Load())
	}
}

// TestSandboxRejectsInvalidJobs refuses oversized source and unsupported identifiers before Docker access.
func TestSandboxRejectsInvalidJobs(t *testing.T) {
	f := DockerFactory{Images: map[string]string{"python": "sha256:" + strings.Repeat("a", 64)}}
	if _, e := f.Open(context.Background(), Job{Language: "python", Source: strings.Repeat("x", 65537)}); e == nil {
		t.Fatal("oversized source")
	}
}

// TestNilContainerCleanup makes cancellation cleanup safe after another path
// has already released and cleared the compiler container.
func TestNilContainerCleanup(t *testing.T) {
	var c *container
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
}

// TestCanceledCreateCleansNamedContainer covers cancellation after the Docker
// daemon accepts create but before its client returns a container identifier.
func TestCanceledCreateCleansNamedContainer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker command requires a POSIX executable")
	}
	dir := t.TempDir()
	cleanup := filepath.Join(dir, "cleanup")
	script := "#!/bin/sh\nif [ \"$1\" = create ]; then\n  echo accepted\n  while :; do :; done\nelif [ \"$1\" = rm ]; then\n  printf '%s\\n' \"$@\" > \"$FAKE_CLEANUP\"\nfi\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_CLEANUP", cleanup)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	image := "sha256:" + strings.Repeat("a", 64)
	if _, err := createContainer(ctx, image, 64); err == nil {
		t.Fatal("canceled create succeeded")
	}
	removed, err := os.ReadFile(cleanup)
	if err != nil || !strings.Contains(string(removed), "judge-") {
		t.Fatalf("canceled create was not cleaned by name: %q %v", removed, err)
	}
}
