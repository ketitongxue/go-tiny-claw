package channel

import "time"

type ChatType string

const (
	ChatDirect ChatType = "direct"
	ChatGroup  ChatType = "group"
)

type InboundMessage struct {
	ID           string
	Route        Route
	UserID       string
	UserName     string
	ChatType     ChatType
	Text         string
	MentionedBot bool
	ReceivedAt   time.Time
}

type OutboundKind string

const (
	OutboundText       OutboundKind = "text"
	OutboundThinking   OutboundKind = "thinking"
	OutboundToolStart  OutboundKind = "tool_start"
	OutboundToolResult OutboundKind = "tool_result"
	OutboundError      OutboundKind = "error"
)

type OutboundMessage struct {
	Kind     OutboundKind
	Text     string
	ToolName string
	IsError  bool
}
