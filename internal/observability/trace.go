// internal/observability/trace.go
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// traceKey 是 Context 中存放 Span 的专属 Key
type traceKey struct{}

// Span 代表链路追踪中的一个时间跨度和操作节点
type Span struct {
	Name       string                 `json:"name"`
	StartTime  time.Time              `json:"start_time"`
	EndTime    time.Time              `json:"end_time"`
	DurationMs int64                  `json:"duration_ms"`
	Attributes map[string]interface{} `json:"attributes,omitempty"` // 存放元数据 (如消耗的 Token, 执行的命令)
	Children   []*Span                `json:"children,omitempty"`   // 子跨度

	mu sync.Mutex // 保护 Children 的并发写入
}

// StartSpan 开启一个新的追踪跨度，并将其级联到 Context 中
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	span := &Span{
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
	}

	// 从 context 中尝试获取父 Span
	if parent, ok := ctx.Value(traceKey{}).(*Span); ok {
		parent.mu.Lock()
		parent.Children = append(parent.Children, span)
		parent.mu.Unlock()
	}

	// 将当前新创建的 Span 作为最新的父节点，塞入衍生 Context 并返回
	newCtx := context.WithValue(ctx, traceKey{}, span)
	return newCtx, span
}

// EndSpan 结束跨度，计算耗时
func (s *Span) EndSpan() {
	s.EndTime = time.Now()
	s.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
}

// AddAttribute 为当前 Span 记录关键的元数据
func (s *Span) AddAttribute(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Attributes[key] = value
}

// ExportTraceToFile 当整个根 Span 结束时，将其序列化并保存为本地 JSON 文件
func ExportTraceToFile(rootSpan *Span, workDir string, sessionID string) error {
	traceDir := filepath.Join(workDir, ".claw", "traces")
	if err := os.MkdirAll(traceDir, 0755); err != nil {
		return err
	}

	// 美化输出 JSON，便于人类和工具阅读
	data, err := json.MarshalIndent(rootSpan, "", "  ")
	if err != nil {
		return err
	}

	// Sessions can finish more than once per second. Create each file atomically
	// with a unique suffix, and keep arbitrary session IDs out of path syntax.
	safeID := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, sessionID)
	if len(safeID) > 80 {
		safeID = safeID[:80]
	}
	file, err := os.CreateTemp(traceDir, fmt.Sprintf("trace_%s_*.json", safeID))
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(file.Name())
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(file.Name())
		return closeErr
	}
	return nil
}
