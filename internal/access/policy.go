// Package access authorizes messaging identities without platform dependencies.
package access

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

type Mode string

const (
	ModeOpen       Mode = "open"
	ModeRestricted Mode = "restricted"
)

// Scope identifies a bot account, not a global cross-platform user identity.
type Scope struct {
	Channel   string
	AccountID string
}

// Policy is construction input only. NewAuthorizer copies it into private sets.
type Policy struct {
	Mode       Mode
	Operators  []string
	Approvers  []string
	GroupChats []string
}

type Reason string

const (
	ReasonAllowed         Reason = "allowed"
	ReasonPolicyNotFound  Reason = "policy_not_found"
	ReasonMissingUserID   Reason = "missing_user_id"
	ReasonUserNotAllowed  Reason = "user_not_allowed"
	ReasonGroupNotAllowed Reason = "group_not_allowed"
	ReasonNotApprover     Reason = "not_approver"
	ReasonInvalidChatType Reason = "invalid_chat_type"
)

type Decision struct {
	Allowed bool
	Reason  Reason
}

type compiledPolicy struct {
	mode       Mode
	operators  map[string]struct{}
	approvers  map[string]struct{}
	groupChats map[string]struct{}
}

// Authorizer is immutable after construction and safe for concurrent reads.
// Missing scopes (including a nil/zero Authorizer) always fail closed.
type Authorizer struct {
	policies map[Scope]compiledPolicy
}

func NewAuthorizer(policies map[Scope]Policy) (*Authorizer, error) {
	a := &Authorizer{policies: make(map[Scope]compiledPolicy, len(policies))}
	for scope, input := range policies {
		scope = normalizeScope(scope)
		if scope.Channel == "" || scope.AccountID == "" {
			return nil, errors.New("access scope requires channel and account")
		}
		if _, exists := a.policies[scope]; exists {
			return nil, errors.New("duplicate normalized access scope")
		}
		p, err := compile(input)
		if err != nil {
			// No supplied IDs or mode values belong in validation errors.
			return nil, err
		}
		a.policies[scope] = p
	}
	return a, nil
}

func compile(input Policy) (compiledPolicy, error) {
	p := compiledPolicy{mode: input.Mode}
	if p.mode == "" {
		p.mode = ModeOpen
	}
	if p.mode != ModeOpen && p.mode != ModeRestricted {
		return p, errors.New("access.mode must be open or restricted")
	}
	var err error
	if p.operators, err = idSet(input.Operators, "operators"); err != nil {
		return p, err
	}
	if p.approvers, err = idSet(input.Approvers, "approvers"); err != nil {
		return p, err
	}
	if p.groupChats, err = idSet(input.GroupChats, "group_chats"); err != nil {
		return p, err
	}
	if p.mode == ModeRestricted && len(p.operators) == 0 {
		return p, errors.New("restricted access requires nonempty operators")
	}
	for id := range p.approvers {
		if !contains(p.operators, id) {
			return p, errors.New("access.approvers must be a subset of operators")
		}
	}
	return p, nil
}

func idSet(ids []string, field string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("access.%s must not contain empty IDs", field)
		}
		set[id] = struct{}{}
	}
	return set, nil
}

func contains(set map[string]struct{}, id string) bool {
	_, ok := set[id]
	return ok
}

func normalizeScope(s Scope) Scope {
	return Scope{Channel: strings.TrimSpace(s.Channel), AccountID: strings.TrimSpace(s.AccountID)}
}

func (a *Authorizer) policy(msg channel.InboundMessage) (compiledPolicy, bool) {
	if a == nil {
		return compiledPolicy{}, false
	}
	p, ok := a.policies[normalizeScope(Scope{Channel: msg.Route.Channel, AccountID: msg.Route.AccountID})]
	return p, ok
}

func (a *Authorizer) CanUse(msg channel.InboundMessage) Decision {
	p, ok := a.policy(msg)
	if !ok {
		return Decision{Reason: ReasonPolicyNotFound}
	}
	if p.mode == ModeOpen {
		return Decision{Allowed: true, Reason: ReasonAllowed}
	}
	id := strings.TrimSpace(msg.UserID)
	if id == "" {
		return Decision{Reason: ReasonMissingUserID}
	}
	if !contains(p.operators, id) {
		return Decision{Reason: ReasonUserNotAllowed}
	}
	switch msg.ChatType {
	case channel.ChatDirect:
	case channel.ChatGroup:
		if !contains(p.groupChats, strings.TrimSpace(msg.Route.ChatID)) {
			return Decision{Reason: ReasonGroupNotAllowed}
		}
	default:
		return Decision{Reason: ReasonInvalidChatType}
	}
	return Decision{Allowed: true, Reason: ReasonAllowed}
}

// CanApprove always includes ordinary sender/chat authorization. Open mode
// intentionally preserves legacy approval behavior; restricted is recommended.
func (a *Authorizer) CanApprove(msg channel.InboundMessage) Decision {
	if d := a.CanUse(msg); !d.Allowed {
		return d
	}
	p, _ := a.policy(msg)
	if p.mode == ModeRestricted && !contains(p.approvers, strings.TrimSpace(msg.UserID)) {
		return Decision{Reason: ReasonNotApprover}
	}
	return Decision{Allowed: true, Reason: ReasonAllowed}
}

// OpenScopes returns a detached, stable snapshot for one-time startup warnings.
func (a *Authorizer) OpenScopes() []Scope {
	var scopes []Scope
	if a == nil {
		return scopes
	}
	for scope, p := range a.policies {
		if p.mode == ModeOpen {
			scopes = append(scopes, scope)
		}
	}
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].Channel != scopes[j].Channel {
			return scopes[i].Channel < scopes[j].Channel
		}
		return scopes[i].AccountID < scopes[j].AccountID
	})
	return scopes
}
