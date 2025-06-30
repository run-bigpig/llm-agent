package tracing

import (
	"context"

	"github.com/run-bigpig/llm-agent/pkg/interfaces"
)

// GenerateWithTools implements interfaces.LLM.GenerateWithTools for LLMMiddleware
func (m *LLMMiddleware) GenerateWithTools(ctx context.Context, prompt string, tools []interfaces.Tool, options ...interfaces.GenerateOption) (string, error) {
	return m.llm.GenerateWithTools(ctx, prompt, tools, options...)
}
