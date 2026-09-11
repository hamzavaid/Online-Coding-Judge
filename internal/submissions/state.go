// Package submissions defines the authoritative judging lifecycle.
package submissions

// CanTransition reports whether a lifecycle edge is permitted; terminal states are immutable.
func CanTransition(from, to string) bool {
	switch from {
	case "QUEUED":
		return to == "CLAIMED" || to == "FAILED_INTERNAL"
	case "CLAIMED":
		return to == "COMPILING" || to == "RUNNING" || to == "FAILED_INTERNAL"
	case "COMPILING":
		return to == "RUNNING" || to == "FINAL" || to == "FAILED_INTERNAL"
	case "RUNNING":
		return to == "FINAL" || to == "FAILED_INTERNAL"
	}
	return false
}
