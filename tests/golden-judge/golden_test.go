package golden

import (
	"context"
	"github.com/hamzavaid/Online-Coding-Judge/internal/judge"
	"github.com/hamzavaid/Online-Coding-Judge/internal/problems"
	"os"
	"testing"
)

// TestGolden certifies basic verdicts for both immutable language images using real Docker execution.
func TestGolden(t *testing.T) {
	if os.Getenv("TEST_DOCKER") != "1" {
		t.Skip("TEST_DOCKER=1 and language image IDs required")
	}
	factory := judge.DockerFactory{Images: map[string]string{"python": os.Getenv("PYTHON_IMAGE"), "cpp": os.Getenv("CPP_IMAGE")}}
	for _, c := range []struct{ name, lang, source, input, expected, verdict string }{
		{"python_sum", "python", "print(sum(map(int,input().split())))", "1 2", "3", "ACCEPTED"},
		{"cpp_sum", "cpp", "#include <iostream>\nint main(){int a,b;std::cin>>a>>b;std::cout<<a+b;}", "1 2", "3", "ACCEPTED"},
		{"python_wrong", "python", "print(4)", "", "3", "WRONG_ANSWER"},
		{"cpp_wrong", "cpp", "#include <iostream>\nint main(){std::cout<<4;}", "", "3", "WRONG_ANSWER"},
		{"python_syntax", "python", "this is invalid python !", "", "", "COMPILATION_ERROR"},
		{"cpp_syntax", "cpp", "invalid c++ !", "", "", "COMPILATION_ERROR"},
		{"python_runtime", "python", "raise RuntimeError('private output')", "", "", "RUNTIME_ERROR"},
		{"cpp_runtime", "cpp", "int main(){return 1;}", "", "", "RUNTIME_ERROR"},
		{"unicode", "python", "print('π')", "", "π", "ACCEPTED"},
	} {
		t.Run(c.name, func(t *testing.T) {
			result, e := (&judge.Engine{Factory: factory}).Evaluate(context.Background(), judge.Job{Language: c.lang, Source: c.source, Problem: problems.Problem{TimeLimitMS: 2000, MemoryLimitMB: 128, Tests: []problems.TestCase{{Input: c.input, Expected: c.expected}, {Input: c.input, Expected: c.expected, Hidden: true}}}}, func(string) error { return nil })
			if e != nil || result.Verdict != c.verdict {
				t.Fatalf("%+v %v", result, e)
			}
		})
	}
}
