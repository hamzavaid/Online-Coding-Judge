package judge

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const outputLimit = 1024 * 1024
const artifactLimit = 16 * 1024 * 1024

// DockerFactory creates isolated compile and per-test containers from immutable image IDs.
// The worker and Docker daemon must share a Linux host with readable cgroup v2 counters.
type DockerFactory struct{ Images map[string]string }

// container owns a Docker instance and its host-side cgroup monitoring path.
type container struct{ id, cgroup string }

// dockerSandbox separates compile resources from fresh, exactly limited runtime containers.
type dockerSandbox struct {
	compile         *container
	image, language string
	memory          int
	limit           time.Duration
	artifact        []byte
}

// sandboxArgs validates budgets and applies mandatory controls without host mounts or credentials.
func sandboxArgs(image string, memory int) ([]string, error) {
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(image) || memory < 32 || memory > 512 {
		return nil, errors.New("invalid image or memory budget")
	}
	// Only /work permits execution, which C++ binaries require; /tmp remains noexec.
	return []string{"create", "--network=none", "--read-only", "--user=65534:65534", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--cpus=1", "--memory=" + strconv.Itoa(memory) + "m", "--memory-swap=" + strconv.Itoa(memory) + "m", "--tmpfs=/work:rw,exec,nosuid,nodev,size=32m,mode=1777", "--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=16m,mode=1777", "--ulimit=fsize=16777216:16777216", "--ulimit=nofile=64:64", "--ulimit=core=0:0", "--log-driver=none", "--label=online-coding-judge.sandbox=true", image}, nil
}

// Open validates source and creates only a compilation container; test data is never uploaded.
func (f DockerFactory) Open(ctx context.Context, job Job) (Sandbox, error) {
	if (job.Language != "python" && job.Language != "cpp") || len(job.Source) < 1 || len(job.Source) > 65536 || job.Problem.TimeLimitMS < 100 || job.Problem.TimeLimitMS > 10000 {
		return nil, errors.New("invalid execution job")
	}
	image := f.Images[job.Language]
	if _, e := sandboxArgs(image, job.Problem.MemoryLimitMB); e != nil {
		return nil, e
	}
	c, e := createContainer(ctx, image, 256)
	if e != nil {
		return nil, e
	}
	name := "main.py"
	if job.Language == "cpp" {
		name = "main.cpp"
	}
	source, e := archive(name, []byte(job.Source), 0644)
	if e == nil {
		e = c.upload(ctx, source)
	}
	if e != nil {
		_ = c.close()
		return nil, e
	}
	return &dockerSandbox{compile: c, image: image, language: job.Language, memory: job.Problem.MemoryLimitMB, limit: time.Duration(job.Problem.TimeLimitMS) * time.Millisecond, artifact: source}, nil
}

// createContainer starts an offline workspace and requires trustworthy host-side OOM counters.
func createContainer(ctx context.Context, image string, memory int) (c *container, err error) {
	args, e := sandboxArgs(image, memory)
	if e != nil {
		return nil, e
	}
	out, e := control(ctx, nil, 4096, args...)
	if e != nil {
		return nil, e
	}
	c = &container{id: strings.TrimSpace(string(out))}
	ok := false
	defer func() {
		if !ok {
			_ = c.close()
		}
	}()
	if _, e = control(ctx, nil, 4096, "start", c.id); e != nil {
		return nil, e
	}
	pidBytes, e := control(ctx, nil, 4096, "inspect", "--format", "{{.State.Pid}}", c.id)
	if e != nil {
		return nil, e
	}
	pid, e := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if e != nil || pid <= 0 {
		return nil, errors.New("invalid sandbox PID")
	}
	membership, e := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if e != nil {
		return nil, errors.New("local cgroup monitoring unavailable")
	}
	for _, line := range strings.Split(string(membership), "\n") {
		if strings.HasPrefix(line, "0::/") {
			relative := strings.TrimPrefix(line, "0::/")
			path := filepath.Clean(filepath.Join("/sys/fs/cgroup", relative))
			if !strings.HasPrefix(path, "/sys/fs/cgroup/") {
				return nil, errors.New("invalid cgroup path")
			}
			c.cgroup = path
			break
		}
	}
	if c.cgroup == "" {
		return nil, errors.New("cgroup v2 required")
	}
	if _, _, e = c.metrics(); e != nil {
		return nil, e
	}
	ok = true
	return c, nil
}

// archive constructs a single regular-file tar with a fixed trusted destination name.
func archive(name string, data []byte, mode int64) ([]byte, error) {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if e := w.WriteHeader(&tar.Header{Name: name, Mode: mode, Uid: 65534, Gid: 65534, Size: int64(len(data))}); e != nil {
		return nil, e
	}
	if _, e := w.Write(data); e != nil {
		return nil, e
	}
	if e := w.Close(); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}

// upload extracts only worker-generated archives as the non-root sandbox user.
func (c *container) upload(ctx context.Context, data []byte) error {
	_, e := control(ctx, data, 4096, "exec", "-i", c.id, "tar", "-x", "--no-same-owner", "-C", "/work")
	return e
}

// Compile syntax-checks Python or builds C++ under an independent 15-second/256-MB budget.
func (s *dockerSandbox) Compile(ctx context.Context) (out Outcome, err error) {
	args := []string{"python3", "-m", "py_compile", "/work/main.py"}
	if s.language == "cpp" {
		args = []string{"g++", "-std=c++23", "-O2", "-o", "/work/main", "/work/main.cpp"}
	}
	out, err = s.compile.execute(ctx, 15*time.Second, "", args...)
	if err != nil || out.ExitCode != 0 || out.TimedOut || out.OOM || out.OutputLimited {
		return out, err
	}
	if s.language == "cpp" {
		data, e := control(ctx, nil, artifactLimit+65536, "exec", s.compile.id, "tar", "-c", "-C", "/work", "main")
		if e != nil {
			return out, e
		}
		r := tar.NewReader(bytes.NewReader(data))
		header, e := r.Next()
		if e != nil || header.Name != "main" || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > artifactLimit {
			return out, errors.New("invalid compiler artifact")
		}
		binary, e := io.ReadAll(io.LimitReader(r, artifactLimit+1))
		if e != nil {
			return out, e
		}
		s.artifact, e = archive("main", binary, 0755)
		if e != nil {
			return out, e
		}
	}
	err = s.compile.close()
	if err == nil {
		s.compile = nil
	}
	return out, err
}

// Run gives each case a clean workspace; compiler memory and previous files cannot affect it.
func (s *dockerSandbox) Run(ctx context.Context, input string) (out Outcome, err error) {
	if len(input) > 65536 {
		return out, errors.New("test input too large")
	}
	c, e := createContainer(ctx, s.image, s.memory)
	if e != nil {
		return out, e
	}
	defer func() {
		if closeErr := c.close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if e = c.upload(ctx, s.artifact); e != nil {
		return out, e
	}
	args := []string{"python3", "-I", "/work/main.py"}
	if s.language == "cpp" {
		args = []string{"/work/main"}
	}
	return c.execute(ctx, s.limit, input, args...)
}

// metrics reads kernel-owned counters on the worker host; submitted code cannot forge them.
func (c *container) metrics() (int, bool, error) {
	peak, e := os.ReadFile(filepath.Join(c.cgroup, "memory.peak"))
	if e != nil {
		return 0, false, errors.New("memory monitoring unavailable")
	}
	n, e := strconv.ParseInt(strings.TrimSpace(string(peak)), 10, 64)
	if e != nil {
		return 0, false, e
	}
	events, e := os.ReadFile(filepath.Join(c.cgroup, "memory.events"))
	if e != nil {
		return 0, false, e
	}
	oom := false
	for _, line := range strings.Split(string(events), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[0] == "oom_kill" || f[0] == "oom_group_kill") {
			count, e := strconv.Atoi(f[1])
			if e != nil {
				return 0, false, e
			}
			oom = oom || count > 0
		}
	}
	return int((n + 1023) / 1024), oom, nil
}

// execute measures from a trusted launch marker, bounds both output streams, and checks cgroup OOM.
func (c *container) execute(parent context.Context, limit time.Duration, input string, args ...string) (Outcome, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	b := newOutputBudget(outputLimit, cancel)
	clock := &launchClock{timer: time.AfterFunc(30*time.Second, cancel), limit: limit, stderr: b.writer(false)}
	defer clock.stop()
	command := []string{"exec", "-i", c.id, "/bin/sh", "-c", "printf 'JUDGE_STARTED\\n' >&2; exec \"$@\"", "judge-run"}
	cmd := exec.CommandContext(ctx, "docker", append(command, args...)...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = b.writer(true)
	cmd.Stderr = clock
	cmd.WaitDelay = time.Second
	e := cmd.Run()
	start := clock.startTime()
	if parent.Err() != nil {
		return Outcome{}, parent.Err()
	}
	if start.IsZero() {
		return Outcome{}, errors.New("runtime did not start")
	}
	out := Outcome{Output: b.output(), RuntimeMS: int(time.Since(start).Milliseconds()), OutputLimited: b.limited(), TimedOut: ctx.Err() != nil && !b.limited()}
	memory, oom, eMetrics := c.metrics()
	if eMetrics != nil {
		return out, eMetrics
	}
	out.MemoryKB = memory
	out.OOM = oom
	if e != nil {
		var exit *exec.ExitError
		if !errors.As(e, &exit) && ctx.Err() == nil {
			return out, errors.New("docker execution failed")
		}
		if exit != nil {
			out.ExitCode = exit.ExitCode()
		}
	}
	return out, nil
}

// Close cleans an unfinished compiler container even after worker cancellation.
func (s *dockerSandbox) Close() error {
	if s.compile == nil {
		return nil
	}
	e := s.compile.close()
	if e == nil {
		s.compile = nil
	}
	return e
}

// close forcibly removes all sandbox processes using a fresh bounded cleanup context.
func (c *container) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, e := control(ctx, nil, 4096, "rm", "-f", c.id)
	return e
}

// control runs fixed Docker control-plane arguments with bounded output and no shell expansion.
func control(parent context.Context, input []byte, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	b := newOutputBudget(limit, cancel)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = b.writer(true)
	cmd.Stderr = b.writer(false)
	cmd.WaitDelay = time.Second
	if e := cmd.Run(); e != nil || b.limited() {
		return nil, fmt.Errorf("docker %s failed", args[0])
	}
	return []byte(b.output()), nil
}

// launchClock consumes the fixed prefix before user execution and forwards only diagnostic bytes.
type launchClock struct {
	mu     sync.Mutex
	prefix []byte
	start  time.Time
	timer  *time.Timer
	limit  time.Duration
	stderr io.Writer
}

// Write arms the deadline once and forwards stderr into the shared output budget.
func (c *launchClock) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	marker := "JUDGE_STARTED\n"
	if c.start.IsZero() {
		needed := len(marker) - len(c.prefix)
		take := min(needed, len(p))
		c.prefix = append(c.prefix, p[:take]...)
		p = p[take:]
		if len(c.prefix) == len(marker) {
			if string(c.prefix) != marker {
				return n, errors.New("invalid launch marker")
			}
			c.start = time.Now()
			c.timer.Reset(c.limit)
		}
	}
	if len(p) > 0 {
		_, e := c.stderr.Write(p)
		return n, e
	}
	return n, nil
}

// startTime reads launch time after Docker's output readers have joined.
func (c *launchClock) startTime() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.start }

// stop prevents a deadline callback from outliving the execution operation.
func (c *launchClock) stop() { c.mu.Lock(); defer c.mu.Unlock(); c.timer.Stop() }
