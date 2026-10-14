package context

// Defaults live here, shared by main agent, Explorer and unknown models.
const DefaultContextWindow = 32768

type ContextPolicy struct {
	DefaultContextWindow  int `json:"default_context_window,omitempty"`
	DefaultReservedOutput int `json:"default_reserved_output,omitempty"`
	SafetyMarginPercent   int `json:"safety_margin_percent,omitempty"`
	MaxToolResultTokens   int `json:"max_tool_result_tokens,omitempty"`
}
type ModelContextSpec struct{ ContextWindow, MaxOutput int }

func (p ContextPolicy) Resolved() ContextPolicy {
	if p.DefaultContextWindow <= 0 {
		p.DefaultContextWindow = DefaultContextWindow
	}
	if p.DefaultReservedOutput <= 0 {
		p.DefaultReservedOutput = 4096
	}
	if p.SafetyMarginPercent <= 0 {
		p.SafetyMarginPercent = 10
	}
	if p.MaxToolResultTokens <= 0 {
		p.MaxToolResultTokens = 4096
	}
	return p
}
func (p ContextPolicy) Budget(m ModelContextSpec) (window, input int) {
	p = p.Resolved()
	window = m.ContextWindow
	if window <= 0 {
		window = p.DefaultContextWindow
	}
	output := m.MaxOutput
	if output <= 0 {
		output = p.DefaultReservedOutput
	}
	input = window - output - (window*p.SafetyMarginPercent+99)/100
	return
}
