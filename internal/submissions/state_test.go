package submissions

import "testing"

// TestTransitions exhaustively rejects unspecified lifecycle changes.
func TestTransitions(t *testing.T) {
	allowed := map[string]bool{"QUEUED/CLAIMED": true, "CLAIMED/COMPILING": true, "CLAIMED/RUNNING": true, "COMPILING/RUNNING": true, "COMPILING/FINAL": true, "RUNNING/FINAL": true, "QUEUED/FAILED_INTERNAL": true, "CLAIMED/FAILED_INTERNAL": true, "COMPILING/FAILED_INTERNAL": true, "RUNNING/FAILED_INTERNAL": true}
	states := []string{"QUEUED", "CLAIMED", "COMPILING", "RUNNING", "FINAL", "FAILED_INTERNAL", "INVALID"}
	for _, a := range states {
		for _, b := range states {
			if CanTransition(a, b) != allowed[a+"/"+b] {
				t.Errorf("%s -> %s", a, b)
			}
		}
	}
}
