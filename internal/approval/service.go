package approval

import (
	"context"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// Service binds middleware to one runtime's actual Session ID (including legacy
// sessions) and immutable reply route. It is explicitly injected, not global.
type Service struct {
	manager   *ApprovalManager
	sessionID string
	route     channel.Route
	notifier  Notifier
}

func NewService(manager *ApprovalManager, sessionID string, route channel.Route, notifier Notifier) *Service {
	return &Service{manager: manager, sessionID: sessionID, route: route, notifier: notifier}
}

func (s *Service) Check(ctx context.Context, call schema.ToolCall) (bool, string) {
	if err := ctx.Err(); err != nil {
		return false, err.Error()
	}
	if !IsDangerousCommand(call.Name, string(call.Arguments)) {
		return true, ""
	}
	if s == nil {
		return false, "审批通道不可用"
	}
	return s.manager.Wait(ctx, Request{SessionID: s.sessionID, Route: s.route, TaskID: call.ID, ToolName: call.Name, Args: string(call.Arguments)}, s.notifier)
}
