package access

import (
	"strings"
	"sync"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

func message(user, chat string, kind channel.ChatType) channel.InboundMessage {
	return channel.InboundMessage{Route: channel.Route{Channel: "telegram", AccountID: "default", ChatID: chat},
		UserID: user, ChatType: kind}
}

func authorizer(t *testing.T, policies map[Scope]Policy) *Authorizer {
	t.Helper()
	a, err := NewAuthorizer(policies)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRestrictedUseAndApproval(t *testing.T) {
	a := authorizer(t, map[Scope]Policy{{"telegram", "default"}: {
		Mode: ModeRestricted, Operators: []string{"100", "200", "ou_Case"}, Approvers: []string{"100"}, GroupChats: []string{"-1001"},
	}})
	for _, tc := range []struct {
		name, user, chat string
		kind             channel.ChatType
		use, approve     Reason
	}{
		{"direct-approver", "100", "private", channel.ChatDirect, ReasonAllowed, ReasonAllowed},
		{"direct-operator", "200", "private", channel.ChatDirect, ReasonAllowed, ReasonNotApprover},
		{"direct-stranger", "300", "private", channel.ChatDirect, ReasonUserNotAllowed, ReasonUserNotAllowed},
		{"group-approver", "100", "-1001", channel.ChatGroup, ReasonAllowed, ReasonAllowed},
		{"group-operator", "200", "-1001", channel.ChatGroup, ReasonAllowed, ReasonNotApprover},
		{"group-stranger", "300", "-1001", channel.ChatGroup, ReasonUserNotAllowed, ReasonUserNotAllowed},
		{"unlisted-group", "100", "-1002", channel.ChatGroup, ReasonGroupNotAllowed, ReasonGroupNotAllowed},
		{"missing-user", "", "100", channel.ChatDirect, ReasonMissingUserID, ReasonMissingUserID},
		{"whitespace-user", " \t", "100", channel.ChatGroup, ReasonMissingUserID, ReasonMissingUserID},
		{"trim-inbound", " 100\t", " -1001 ", channel.ChatGroup, ReasonAllowed, ReasonAllowed},
		{"case-preserved", "ou_Case", "private", channel.ChatDirect, ReasonAllowed, ReasonNotApprover},
		{"case-sensitive", "ou_case", "private", channel.ChatDirect, ReasonUserNotAllowed, ReasonUserNotAllowed},
		{"invalid-chat-type", "100", "private", "unknown", ReasonInvalidChatType, ReasonInvalidChatType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := message(tc.user, tc.chat, tc.kind)
			msg.UserName = "100" // Never a fallback identity.
			for _, check := range []struct {
				got  Decision
				want Reason
			}{{a.CanUse(msg), tc.use}, {a.CanApprove(msg), tc.approve}} {
				if check.got.Reason != check.want || check.got.Allowed != (check.want == ReasonAllowed) {
					t.Fatalf("decision=%+v want=%s", check.got, check.want)
				}
			}
		})
	}
}

func TestOpenCompatibilityAndEmptyRestrictedLists(t *testing.T) {
	for _, mode := range []Mode{"", ModeOpen} {
		a := authorizer(t, map[Scope]Policy{{"telegram", "default"}: {Mode: mode}})
		for _, kind := range []channel.ChatType{channel.ChatDirect, channel.ChatGroup} {
			msg := message("", "unlisted", kind)
			if !a.CanUse(msg).Allowed || !a.CanApprove(msg).Allowed || len(a.OpenScopes()) != 1 {
				t.Fatal("legacy open behavior changed")
			}
		}
	}
	a := authorizer(t, map[Scope]Policy{{"telegram", "default"}: {Mode: ModeRestricted, Operators: []string{"100"}}})
	msg := message("100", "unlisted", channel.ChatDirect)
	if !a.CanUse(msg).Allowed || a.CanApprove(msg).Reason != ReasonNotApprover {
		t.Fatal("empty approvers must permit tasks but deny every approval")
	}
	msg.ChatType = channel.ChatGroup
	if a.CanUse(msg).Reason != ReasonGroupNotAllowed || a.CanApprove(msg).Allowed {
		t.Fatal("empty groups must allow only direct chats")
	}
}

func TestChannelAndAccountIsolation(t *testing.T) {
	a := authorizer(t, map[Scope]Policy{
		{"telegram", "default"}: {Mode: ModeRestricted, Operators: []string{"42"}, Approvers: []string{"42"}},
		{"feishu", "default"}:   {Mode: ModeRestricted, Operators: []string{"other"}},
		{"telegram", "second"}:  {Mode: ModeRestricted, Operators: []string{"other"}},
	})
	for _, tc := range []struct {
		channel, account string
		want             Reason
	}{{"telegram", "default", ReasonAllowed}, {"feishu", "default", ReasonUserNotAllowed},
		{"telegram", "second", ReasonUserNotAllowed}, {"telegram", "missing", ReasonPolicyNotFound}, {"missing", "default", ReasonPolicyNotFound}} {
		msg := message("42", "private", channel.ChatDirect)
		msg.Route.Channel, msg.Route.AccountID = tc.channel, tc.account
		if a.CanUse(msg).Reason != tc.want || a.CanApprove(msg).Reason != tc.want {
			t.Fatal("scope mixed", tc)
		}
	}
	msg := message("42", "private", channel.ChatDirect)
	for _, a := range []*Authorizer{nil, {}, authorizer(t, nil)} {
		if a.CanUse(msg).Allowed || a.CanApprove(msg).Allowed {
			t.Fatal("unconfigured authorizer failed open")
		}
	}
}

func TestPolicyValidation(t *testing.T) {
	for name, p := range map[string]Policy{
		"unknown-mode":      {Mode: "secret-must-not-appear"},
		"case-mode":         {Mode: "OPEN"},
		"missing-operators": {Mode: ModeRestricted},
		"empty-operator":    {Operators: []string{"100", " \t"}},
		"empty-approver":    {Approvers: []string{""}},
		"empty-group":       {GroupChats: []string{" \n"}},
		"not-subset":        {Mode: ModeRestricted, Operators: []string{"100"}, Approvers: []string{"secret-must-not-appear"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewAuthorizer(map[Scope]Policy{{"telegram", "default"}: p})
			if err == nil || strings.Contains(err.Error(), "secret-must-not-appear") {
				t.Fatal("invalid policy accepted or leaked identity", err)
			}
		})
	}
	for _, policies := range []map[Scope]Policy{
		{{"", "default"}: {}}, {{"telegram", " \t"}: {}},
		{{"telegram", "default"}: {}, {" telegram ", " default "}: {}},
	} {
		if _, err := NewAuthorizer(policies); err == nil {
			t.Fatal("invalid/duplicate normalized scope accepted")
		}
	}
}

func TestNormalizationDedupAndImmutableConcurrentPolicy(t *testing.T) {
	p := Policy{Mode: ModeRestricted, Operators: []string{" 100 ", "100"}, Approvers: []string{"100", " 100 "}, GroupChats: []string{" -1001 ", "-1001"}}
	input := map[Scope]Policy{{" telegram ", " default "}: p, {"open", "default"}: {}}
	a := authorizer(t, input)
	compiled := a.policies[Scope{"telegram", "default"}]
	if len(compiled.operators) != 1 || len(compiled.approvers) != 1 || len(compiled.groupChats) != 1 {
		t.Fatal("normalized IDs were not deduplicated")
	}
	scopes := a.OpenScopes()
	scopes[0].Channel = "changed"
	if a.OpenScopes()[0].Channel != "open" {
		t.Fatal("mutable warning scope snapshot")
	}
	// Mutate all construction input concurrently with repeated authorization.
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			p.Operators[0], p.Approvers[0], p.GroupChats[0] = "changed", "changed", "changed"
			delete(input, Scope{" telegram ", " default "})
		}
	})
	for range 8 {
		wg.Go(func() {
			for range 100 {
				msg := message("100", "-1001", channel.ChatGroup)
				if !a.CanUse(msg).Allowed || !a.CanApprove(msg).Allowed {
					t.Error("construction input mutated live policy")
				}
			}
		})
	}
	wg.Wait()
}
