package gateway

import (
	"log"
	"strings"

	"github.com/ketitongxue/go-tiny-claw/internal/approval"
	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

type commandRouter struct {
	approvals     *approval.ApprovalManager
	conversations *ConversationManager
}

// handle runs before group admission and enqueue: the worker may currently be
// waiting on this exact reply. Malformed control commands are still consumed,
// preserving the previous behavior instead of accidentally prompting the agent.
func (r commandRouter) handle(msg channel.InboundMessage) bool {
	fields := strings.Fields(msg.Text)
	if len(fields) == 0 || (fields[0] != "approve" && fields[0] != "reject") {
		return false
	}
	if len(fields) == 2 {
		allowed := fields[0] == "approve"
		reason := "人类管理员已批准操作"
		if !allowed {
			reason = "人类管理员已拒绝操作"
		}
		key := approval.Key{SessionID: r.conversations.sessionID(msg.Route), TaskID: fields[1]}
		resolved := r.approvals.Resolve(key, allowed, reason)
		log.Printf("[Gateway] 审批指令已处理: matched=%t allowed=%t", resolved, allowed)
	}
	return true
}
