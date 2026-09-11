package security

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
)

// factory requires explicit opt-in because these tests execute hostile programs in Docker.
func factory(t *testing.T) judge.DockerFactory {
	t.Helper()
	if os.Getenv("TEST_DOCKER") != "1" {
		t.Skip("TEST_DOCKER=1 and pinned images required")
	}
	return judge.DockerFactory{Images: map[string]string{"python": os.Getenv("PYTHON_IMAGE"), "cpp": os.Getenv("CPP_IMAGE")}}
}

// evaluate runs one bounded hostile program through the production judge engine.
func evaluate(t *testing.T, lang, source, input, expected string, tests int) judge.Result {
	t.Helper()
	f := factory(t)
	cases := make([]problems.TestCase, tests)
	for i := range cases {
		cases[i] = problems.TestCase{Input: input, Expected: expected, Hidden: i > 0}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, e := (&judge.Engine{Factory: f}).Evaluate(ctx, judge.Job{Language: lang, Source: source, Problem: problems.Problem{TimeLimitMS: 1000, MemoryLimitMB: 64, Tests: cases}}, func(string) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	return result
}

// TestResourceVerdicts checks actual cgroup enforcement and bounded worker output for both languages.
func TestResourceVerdicts(t *testing.T) {
	factory(t)
	for _, c := range []struct{ name, lang, source, want string }{
		{"python_timeout", "python", "while True: pass", "TIME_LIMIT_EXCEEDED"},
		{"cpp_timeout", "cpp", "int main(){for(;;){}}", "TIME_LIMIT_EXCEEDED"},
		{"python_memory", "python", "x=[bytearray(1024*1024) for _ in range(320)]", "MEMORY_LIMIT_EXCEEDED"},
		{"cpp_memory", "cpp", "#include <vector>\n#include <cstring>\nint main(){std::vector<char*> v;for(int i=0;i<320;i++){auto p=new char[1048576];memset(p,1,1048576);v.push_back(p);}}", "MEMORY_LIMIT_EXCEEDED"},
		{"python_stdout", "python", "import sys\nsys.stdout.write('x'*2097152)", "OUTPUT_LIMIT_EXCEEDED"},
		{"python_stderr", "python", "import sys\nsys.stderr.write('x'*2097152)", "OUTPUT_LIMIT_EXCEEDED"},
		{"cpp_stdout", "cpp", "#include <iostream>\nint main(){for(int i=0;i<2097152;i++)std::cout<<'x';}", "OUTPUT_LIMIT_EXCEEDED"},
		{"cpp_stderr", "cpp", "#include <cstdio>\nint main(){char b[4096]={};for(int i=0;i<512;i++)fwrite(b,1,sizeof b,stderr);}", "OUTPUT_LIMIT_EXCEEDED"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := evaluate(t, c.lang, c.source, "", "", 1)
			if r.Verdict != c.want {
				t.Fatalf("%+v want %s", r, c.want)
			}
			if r.MemoryKB <= 0 || r.MemoryKB > 64*1024+1024 {
				t.Fatalf("memory measurement outside sandbox budget: %+v", r)
			}
		})
	}
}

// TestIsolation checks real UID, seccomp, capability, filesystem, network, and secret boundaries.
func TestIsolation(t *testing.T) {
	t.Setenv("JUDGE_HOST_SECRET", "must-not-enter-sandbox")
	source := `import os, socket, errno, resource
assert os.getuid()==65534
assert 'JUDGE_HOST_SECRET' not in os.environ
assert 'DATABASE_URL' not in os.environ
assert not os.path.exists('/var/run/docker.sock')
assert not os.path.exists('/var/run/secrets/kubernetes.io/serviceaccount/token')
status=dict(line.split(':',1) for line in open('/proc/self/status') if ':' in line)
assert int(status['CapEff'].strip(),16)==0
assert status['NoNewPrivs'].strip()=='1'
assert status['Seccomp'].strip()=='2'
try:
 open('/etc/judge-escape','w').write('x')
 raise AssertionError('writable root')
except OSError as e:
 assert e.errno in (errno.EROFS,errno.EACCES)
s=socket.socket(); s.settimeout(.2)
try:
 s.connect(('198.51.100.1',80))
 raise AssertionError('network available')
except OSError as e:
 assert e.errno==errno.ENETUNREACH
assert resource.getrlimit(resource.RLIMIT_FSIZE)[0]==16777216
try:
 with open('/work/flood','wb') as f:
  for _ in range(20): f.write(b'x'*1048576)
 raise AssertionError('file limit missing')
except OSError as e:
 assert e.errno in (errno.EFBIG,errno.ENOSPC)
print('isolated')
`
	r := evaluate(t, "python", source, "", "isolated", 1)
	if r.Verdict != "ACCEPTED" {
		t.Fatal(r)
	}
}

// TestForkContainment proves process creation hits the cgroup ceiling and leaves the worker usable.
func TestForkContainment(t *testing.T) {
	r := evaluate(t, "python", `import os,time
children=[]
try:
 for _ in range(128):
  pid=os.fork()
  if pid==0:
   time.sleep(.2);os._exit(0)
  children.append(pid)
 print('unbounded')
except OSError:
 print('bounded')
finally:
 for pid in children: os.waitpid(pid,0)
`, "", "bounded", 1)
	if r.Verdict != "ACCEPTED" {
		t.Fatal(r)
	}
	r = evaluate(t, "python", "print(42)", "", "42", 1)
	if r.Verdict != "ACCEPTED" {
		t.Fatal("worker unusable", r)
	}
}

// TestFreshTestWorkspace rejects files left behind by earlier test executions.
func TestFreshTestWorkspace(t *testing.T) {
	r := evaluate(t, "python", "import os\nassert not os.path.exists('/work/leftover')\nopen('/work/leftover','w').write('x')\nprint('clean')", "", "clean", 2)
	if r.Verdict != "ACCEPTED" {
		t.Fatal(r)
	}
}

// TestMaximumInput verifies bounded valid input and literal source characters survive transport.
func TestMaximumInput(t *testing.T) {
	r := evaluate(t, "python", "import sys\n# $(touch /etc/escape); `id`\nprint(len(sys.stdin.read()))", strings.Repeat("x", 65536), "65536", 1)
	if r.Verdict != "ACCEPTED" {
		t.Fatal(r)
	}
}

// TestCancellation requires prompt teardown even when the worker context is canceled mid-run.
func TestCancellation(t *testing.T) {
	f := factory(t)
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	_, e := (&judge.Engine{Factory: f}).Evaluate(ctx, judge.Job{Language: "python", Source: "while True: pass", Problem: problems.Problem{TimeLimitMS: 10000, MemoryLimitMB: 64, Tests: []problems.TestCase{{}}}}, func(state string) error {
		if state == "RUNNING" {
			time.AfterFunc(100*time.Millisecond, cancel)
		}
		return nil
	})
	cancel()
	if e == nil || time.Since(start) > 20*time.Second {
		t.Fatalf("cancellation %v after %s", e, time.Since(start))
	}
}
