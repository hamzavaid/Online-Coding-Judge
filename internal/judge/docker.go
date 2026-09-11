package judge

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DockerFactory creates non-root, offline containers from immutable local image IDs.
type DockerFactory struct{ Images map[string]string }

// dockerSandbox owns one container; it never mounts host directories or passes host environment.
type dockerSandbox struct {
	id, language string
	limit        time.Duration
}

// Open uploads only source through a tar stream; expected outputs never enter the container.
func (f DockerFactory) Open(ctx context.Context, job Job) (Sandbox, error) {
	image := f.Images[job.Language]
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(image) {
		return nil, errors.New("immutable local image ID required")
	}
	if job.Language != "python" && job.Language != "cpp" {
		return nil, errors.New("unsupported language")
	}
	budget := max(job.Problem.MemoryLimitMB, 256)
	args := []string{"create", "--network=none", "--user=65534:65534", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--cpus=1", "--memory=" + strconv.Itoa(budget) + "m", "--memory-swap=" + strconv.Itoa(budget) + "m", "--label=online-coding-judge.sandbox=true", image}
	out, e := dockerCommand(ctx, nil, args...)
	if e != nil {
		return nil, e
	}
	s := &dockerSandbox{id: strings.TrimSpace(string(out)), language: job.Language, limit: time.Duration(job.Problem.TimeLimitMS) * time.Millisecond}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	if _, e = dockerCommand(ctx, nil, "start", s.id); e != nil {
		return nil, e
	}
	name := "main.py"
	if job.Language == "cpp" {
		name = "main.cpp"
	}
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	if e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Uid: 65534, Gid: 65534, Size: int64(len(job.Source))}); e != nil {
		return nil, e
	}
	if _, e = tw.Write([]byte(job.Source)); e != nil {
		return nil, e
	}
	if e = tw.Close(); e != nil {
		return nil, e
	}
	if _, e = dockerCommand(ctx, data.Bytes(), "cp", "-", s.id+":/work"); e != nil {
		return nil, e
	}
	ok = true
	return s, nil
}

// Compile validates Python syntax or builds C++ with a separate wall-clock budget.
func (s *dockerSandbox) Compile(ctx context.Context) (Outcome, error) {
	args := []string{"python3", "-m", "py_compile", "/work/main.py"}
	if s.language == "cpp" {
		args = []string{"g++", "-std=c++23", "-O2", "-o", "/work/main", "/work/main.cpp"}
	}
	return s.execute(ctx, 15*time.Second, "", args...)
}

// Run executes a test with a worker-level deadline; stdin contains only the current test input.
func (s *dockerSandbox) Run(ctx context.Context, input string) (Outcome, error) {
	args := []string{"python3", "-I", "/work/main.py"}
	if s.language == "cpp" {
		args = []string{"/work/main"}
	}
	return s.execute(ctx, s.limit, input, args...)
}

// execute separates Docker infrastructure errors from user process exit codes.
func (s *dockerSandbox) execute(parent context.Context, limit time.Duration, input string, args ...string) (Outcome, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// The fixed shim marks actual process launch. Source and input are never shell arguments.
	command := []string{"exec", "-i", s.id, "/bin/sh", "-c", "printf 'JUDGE_STARTED\\n' >&2; exec \"$@\"", "judge-run"}
	cmd := exec.CommandContext(ctx, "docker", append(command, args...)...)
	cmd.Stdin = strings.NewReader(input)
	var stdout bytes.Buffer
	clock := &launchClock{timer: time.AfterFunc(30*time.Second, cancel), limit: limit}
	defer clock.stop()
	cmd.Stdout = &stdout
	cmd.Stderr = clock
	e := cmd.Run()
	start := clock.startTime()
	result := Outcome{Output: stdout.String()}
	if start.IsZero() {
		return result, errors.New("runtime did not start")
	}
	result.RuntimeMS = int(time.Since(start).Milliseconds())
	if parent.Err() != nil {
		return result, parent.Err()
	}
	if ctx.Err() != nil {
		result.TimedOut = true
		return result, nil
	}
	if e != nil {
		var exit *exec.ExitError
		if !errors.As(e, &exit) {
			return result, errors.New("docker execution failed")
		}
		result.ExitCode = exit.ExitCode()
		if result.ExitCode == 125 || result.ExitCode == 126 || result.ExitCode == 127 {
			return result, errors.New("runtime unavailable")
		}
	}
	return result, nil
}

// launchClock recognizes the trusted shim prefix before user code starts and arms its deadline.
type launchClock struct {
	mu     sync.Mutex
	prefix string
	start  time.Time
	timer  *time.Timer
	limit  time.Duration
}

// Write consumes stderr without retaining diagnostics and starts the deadline exactly once.
func (c *launchClock) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.start.IsZero() {
		c.prefix += string(p[:min(len(p), 32)])
		if strings.HasPrefix(c.prefix, "JUDGE_STARTED\n") {
			c.start = time.Now()
			c.timer.Reset(c.limit)
		}
	}
	return len(p), nil
}

// startTime reads the launch instant after command output readers have joined.
func (c *launchClock) startTime() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.start }

// stop prevents the deadline callback from outliving the command.
func (c *launchClock) stop() { c.mu.Lock(); defer c.mu.Unlock(); c.timer.Stop() }

// Close uses an independent timeout so canceled jobs still remove their container and processes.
func (s *dockerSandbox) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, e := dockerCommand(ctx, nil, "rm", "-f", s.id)
	return e
}

// dockerCommand executes fixed control-plane arguments without a shell or raw diagnostic exposure.
func dockerCommand(parent context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(input)
	out, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("docker %s failed: %w", args[0], e)
	}
	return out, nil
}
