package delivery

import "testing"

func TestParseReportMode(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  ReportMode
	}{
		{"", ReportModeCompact},
		{"quiet", ReportModeQuiet},
		{"compact", ReportModeCompact},
		{"verbose", ReportModeVerbose},
	} {
		t.Run("valid/"+tc.value, func(t *testing.T) {
			got, err := ParseReportMode(tc.value)
			if err != nil || got != tc.want {
				t.Fatalf("ParseReportMode(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
			}
		})
	}
	for _, value := range []string{"comapct", "debug", "COMPACT", " compact", "quiet ", " ", "\n"} {
		t.Run("invalid/"+value, func(t *testing.T) {
			if mode, err := ParseReportMode(value); err == nil || mode != "" {
				t.Fatalf("invalid mode %q silently accepted as %q: %v", value, mode, err)
			}
		})
	}
}

func TestReportingPolicy(t *testing.T) {
	events := []struct {
		name string
		show func(ReportingPolicy) bool
		want [3]bool // quiet, compact, verbose
	}{
		{"accepted", ReportingPolicy.ShowAccepted, [3]bool{false, true, false}},
		{"thinking", ReportingPolicy.ShowThinking, [3]bool{false, false, true}},
		{"tool_call", ReportingPolicy.ShowToolCall, [3]bool{false, false, true}},
		{"tool_success", func(p ReportingPolicy) bool { return p.ShowToolResult(false) }, [3]bool{false, false, true}},
		{"tool_error", func(p ReportingPolicy) bool { return p.ShowToolResult(true) }, [3]bool{false, false, true}},
		{"final", ReportingPolicy.ShowFinal, [3]bool{true, true, true}},
		{"fatal_error", ReportingPolicy.ShowError, [3]bool{true, true, true}},
		{"approval", ReportingPolicy.ShowApproval, [3]bool{true, true, true}},
	}
	for i, mode := range []ReportMode{ReportModeQuiet, ReportModeCompact, ReportModeVerbose, ""} {
		t.Run(string(mode), func(t *testing.T) {
			column := i
			if mode == "" {
				column = 1 // A zero-value policy must behave exactly like compact.
			}
			for _, event := range events {
				t.Run(event.name, func(t *testing.T) {
					if got := event.show(ReportingPolicy{Mode: mode}); got != event.want[column] {
						t.Fatalf("%s in %q = %t; want %t", event.name, mode, got, event.want[column])
					}
				})
			}
		})
	}
}
