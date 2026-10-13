package executor

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestPolicyPresets(t *testing.T) {
	coding, explorer := CodingPolicy(), ExplorerPolicy()
	if coding.WorkspaceMode != WorkspaceReadWrite || coding.NetworkEnabled || coding.MemoryMB != 1024 || coding.CPUs != 2 || coding.Pids != 128 || coding.Timeout != 30*time.Second {
		t.Fatalf("coding=%+v", coding)
	}
	if explorer.WorkspaceMode != WorkspaceReadOnly || explorer.NetworkEnabled || explorer.MemoryMB != 512 || explorer.CPUs != 1 || explorer.Pids != 64 || explorer.Timeout != 30*time.Second {
		t.Fatalf("explorer=%+v", explorer)
	}
	coding.AllowedEnv[0] = "SECRET"
	if ExplorerPolicy().AllowedEnv[0] != "PATH" || CodingPolicy().AllowedEnv[0] != "PATH" || explorer.AllowedEnv[0] != "PATH" {
		t.Fatal("policy presets share mutable environment allowlists")
	}
}

func TestPolicyValidation(t *testing.T) {
	for _, mutate := range []func(*SandboxPolicy){
		func(p *SandboxPolicy) { p.WorkspaceMode = "" },
		func(p *SandboxPolicy) { p.MemoryMB = 0 },
		func(p *SandboxPolicy) { p.CPUs = -1 },
		func(p *SandboxPolicy) { p.CPUs = math.NaN() },
		func(p *SandboxPolicy) { p.CPUs = math.Inf(1) },
		func(p *SandboxPolicy) { p.Pids = 0 },
		func(p *SandboxPolicy) { p.Timeout = 0 },
		func(p *SandboxPolicy) { p.AllowedEnv = []string{"TOKEN=bad"} },
		func(p *SandboxPolicy) { p.AllowedEnv = []string{"1BAD"} },
	} {
		policy := CodingPolicy()
		mutate(&policy)
		if err := policy.Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("invalid policy accepted: %+v (%v)", policy, err)
		}
	}
	if err := CodingPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyEnvironment(t *testing.T) {
	policy := CodingPolicy()
	list, err := policy.environment(map[string]string{"LANG": "C.UTF-8", "TERM": "xterm"})
	if err != nil || !reflect.DeepEqual(list, []string{"LANG=C.UTF-8", "TERM=xterm"}) {
		t.Fatalf("list=%v error=%v", list, err)
	}
	list, err = policy.environment(nil)
	if err != nil || len(list) != 0 {
		t.Fatalf("environment inherited without a request: %v (%v)", list, err)
	}
	for _, key := range []string{"ZHIPU_API_KEY", "GITHUB_TOKEN", "AWS_ACCESS_KEY_ID", "SSH_AUTH_SOCK", "FEISHU_APP_SECRET", "LANG=bad"} {
		if _, err := policy.environment(map[string]string{key: "secret"}); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("disallowed key %q accepted: %v", key, err)
		}
	}
	if _, err := policy.environment(map[string]string{"LANG": "bad\x00value"}); err == nil {
		t.Fatal("accepted NUL environment value")
	}
}

func TestPolicyTimeoutCannotBeRaisedByRequest(t *testing.T) {
	policy := CodingPolicy()
	for _, tc := range []struct{ request, want time.Duration }{
		{0, policy.Timeout}, {time.Second, time.Second}, {time.Hour, policy.Timeout},
	} {
		got, err := policy.executionTimeout(tc.request)
		if err != nil || got != tc.want {
			t.Fatalf("requested=%v got=%v want=%v error=%v", tc.request, got, tc.want, err)
		}
	}
	if _, err := policy.executionTimeout(-time.Second); err == nil {
		t.Fatal("negative timeout accepted")
	}
}
