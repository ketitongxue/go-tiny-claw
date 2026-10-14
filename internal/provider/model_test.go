package provider

import "testing"

func TestParseModelRef(t *testing.T) {
	for _, value := range []string{"openai/model", "zai-coding-cn/glm-5.3", "openrouter/anthropic/claude-x"} {
		ref, err := ParseModelRef(value)
		if err != nil || ref.String() != value {
			t.Fatalf("%s: %v %v", value, ref, err)
		}
	}
	for _, value := range []string{"/model", "openai/", "openai", "", "bad id/model", "../model", "OpenAI/model", "a$/model", "openai/\nsecret", "openai/ model"} {
		if _, err := ParseModelRef(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestCapabilitiesAndBilling(t *testing.T) {
	model := ModelInfo{ToolCalling: true, Reasoning: true}
	if model.Capabilities() != (Capabilities{ToolCalling: true, Reasoning: true}) {
		t.Fatal("capabilities lost")
	}
	if got := (UsageBilling{Mode: BillingSubscription}).String(); got != "计费模式: Subscription" {
		t.Fatal(got)
	}
}
