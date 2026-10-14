package provider

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func ValidateProviderID(id string) error {
	if !providerIDPattern.MatchString(id) {
		return fmt.Errorf("invalid provider ID %q: expected [a-z0-9][a-z0-9_-]*", id)
	}
	return nil
}

type ModelRef struct {
	ProviderID string
	ModelID    string
}

func ParseModelRef(value string) (ModelRef, error) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return ModelRef{}, fmt.Errorf("invalid model reference %q: expected provider/model", value)
	}
	ref := ModelRef{ProviderID: parts[0], ModelID: parts[1]}
	if err := ValidateProviderID(ref.ProviderID); err != nil {
		return ModelRef{}, err
	}
	if ref.ModelID == "" || strings.ContainsFunc(ref.ModelID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return ModelRef{}, fmt.Errorf("invalid model ID in %q", value)
	}
	return ref, nil
}

func (r ModelRef) String() string { return r.ProviderID + "/" + r.ModelID }

type BillingMode string

const (
	BillingAPI          BillingMode = "api"
	BillingSubscription BillingMode = "subscription"
	BillingUnknown      BillingMode = "unknown"
)

type Capabilities struct {
	ToolCalling bool
	Reasoning   bool
}

// Pricing is optional, currency-specific pricing per million tokens. A nil
// price is unknown, never an assertion that usage is free.
type Pricing struct {
	Input, Output, CachedInput float64
	Currency                   string
}

type ModelInfo struct {
	ProviderID    string      `json:"-"`
	ID            string      `json:"id"`
	Name          string      `json:"name,omitempty"`
	ToolCalling   bool        `json:"tool_calling"`
	Reasoning     bool        `json:"reasoning,omitempty"`
	ContextWindow int         `json:"context_window,omitempty"`
	MaxOutput     int         `json:"max_output,omitempty"`
	BillingMode   BillingMode `json:"billing,omitempty"`
	Pricing       *Pricing    `json:"-"`
}

func (m ModelInfo) Ref() ModelRef { return ModelRef{ProviderID: m.ProviderID, ModelID: m.ID} }
func (m ModelInfo) Capabilities() Capabilities {
	return Capabilities{ToolCalling: m.ToolCalling, Reasoning: m.Reasoning}
}

type UsageBilling struct {
	Mode          BillingMode
	EstimatedCost float64
	CostKnown     bool
	Currency      string
}

func (b UsageBilling) String() string {
	if b.Mode == BillingSubscription {
		return "计费模式: Subscription"
	}
	if !b.CostKnown {
		return fmt.Sprintf("计费模式: %s | Cost: unknown", b.Mode)
	}
	return fmt.Sprintf("计费模式: %s | 估算费用 (%s): %.6f", b.Mode, b.Currency, b.EstimatedCost)
}
