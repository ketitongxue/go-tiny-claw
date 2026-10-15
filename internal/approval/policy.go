package approval

import "regexp"

// IsDangerousCommand is a heuristic Human Approval Layer, not a Security
// Boundary. Sandbox permissions still apply even if a regex misses a command
// or a human approves it. This preserves the existing policy unchanged.
func IsDangerousCommand(toolName, args string) bool {
	if toolName != "bash" {
		return false
	}
	for _, pattern := range dangerousPatterns {
		if pattern.MatchString(args) {
			return true
		}
	}
	return false
}

var dangerousPatterns = []*regexp.Regexp{
	regexp.MustCompile(`rm\s+-r`),
	regexp.MustCompile(`sudo\s+`),
	regexp.MustCompile(`drop\s+`),
	regexp.MustCompile(`>.*\.go`),
}
