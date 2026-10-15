package delivery

import "fmt"

// ReportMode controls messaging presentation, not engine execution or tracing.
type ReportMode string

const (
	ReportModeQuiet   ReportMode = "quiet"
	ReportModeCompact ReportMode = "compact"
	ReportModeVerbose ReportMode = "verbose"
)

// ParseReportMode defaults only an omitted/empty value. Typos are errors.
func ParseReportMode(value string) (ReportMode, error) {
	switch ReportMode(value) {
	case "", ReportModeCompact:
		return ReportModeCompact, nil
	case ReportModeQuiet:
		return ReportModeQuiet, nil
	case ReportModeVerbose:
		return ReportModeVerbose, nil
	default:
		return "", fmt.Errorf("invalid messaging.report_mode %q: want quiet, compact or verbose", value)
	}
}

// ReportingPolicy's zero value is compact, including for embedded Gateways.
// Final output, task-level errors and approval are never telemetry.
type ReportingPolicy struct {
	Mode ReportMode
}

func (p ReportingPolicy) ShowAccepted() bool {
	return p.Mode == "" || p.Mode == ReportModeCompact
}

func (p ReportingPolicy) ShowThinking() bool { return p.Mode == ReportModeVerbose }
func (p ReportingPolicy) ShowToolCall() bool { return p.Mode == ReportModeVerbose }

// Tool errors can be recovered by the agent; they are not task-level errors.
func (p ReportingPolicy) ShowToolResult(isError bool) bool { return p.Mode == ReportModeVerbose }

func (p ReportingPolicy) ShowFinal() bool    { return true }
func (p ReportingPolicy) ShowError() bool    { return true }
func (p ReportingPolicy) ShowApproval() bool { return true }
