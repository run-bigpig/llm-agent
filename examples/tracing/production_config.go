package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/run-bigpig/llm-agent/pkg/agent"
	"github.com/run-bigpig/llm-agent/pkg/llm/openai"
	"github.com/run-bigpig/llm-agent/pkg/logging"
	"github.com/run-bigpig/llm-agent/pkg/memory"
	"github.com/run-bigpig/llm-agent/pkg/tracing"
)

func setupProductionTracing(logger logging.Logger, ctx context.Context) (*tracing.LangfuseTracer, *tracing.OTelTracer, error) {
	// Initialize Langfuse tracer
	langfuseTracer, err := tracing.NewLangfuseTracer(tracing.LangfuseConfig{
		Enabled:     true,
		SecretKey:   os.Getenv("LANGFUSE_SECRET_KEY"),
		PublicKey:   os.Getenv("LANGFUSE_PUBLIC_KEY"),
		Environment: os.Getenv("ENVIRONMENT"),
	})
	if err != nil {
		logger.Error(ctx, "Failed to initialize Langfuse tracer", map[string]interface{}{"error": err.Error()})
		return nil, nil, fmt.Errorf("failed to initialize Langfuse tracer: %w", err)
	}
	logger.Info(ctx, "Langfuse tracer initialized", nil)

	// Initialize OpenTelemetry tracer
	otelTracer, err := tracing.NewOTelTracer(tracing.OTelConfig{
		Enabled:           true,
		ServiceName:       os.Getenv("SERVICE_NAME"),
		CollectorEndpoint: os.Getenv("OTEL_COLLECTOR_ENDPOINT"),
	})
	if err != nil {
		logger.Error(ctx, "Failed to initialize OpenTelemetry tracer", map[string]interface{}{"error": err.Error()})
		return nil, nil, fmt.Errorf("failed to initialize OpenTelemetry tracer: %w", err)
	}
	logger.Info(ctx, "OpenTelemetry tracer initialized", nil)

	return langfuseTracer, otelTracer, nil
}

func CreateTracedAgent(ctx context.Context) (*agent.Agent, context.Context, error) {
	// Create a logger
	logger := logging.New()

	// Setup tracing
	langfuseTracer, otelTracer, err := setupProductionTracing(logger, ctx)
	if err != nil {
		return nil, ctx, err
	}

	// Add conversation ID to context
	conversationID := fmt.Sprintf("conv-%d", time.Now().UnixNano())
	ctx = memory.WithConversationID(ctx, conversationID)
	logger.Info(ctx, "Added conversation ID to context", map[string]interface{}{"conversation_id": conversationID})

	// Create LLM client with tracing
	llm := openai.NewClient(os.Getenv("OPENAI_API_KEY"),
		openai.WithModel(os.Getenv("LLM_MODEL")),
		openai.WithLogger(logger),
	)
	llmWithLangfuse := tracing.NewLLMMiddleware(llm, langfuseTracer)
	llmWithOTel := tracing.NewLLMOTelMiddleware(llmWithLangfuse, otelTracer)
	logger.Info(ctx, "Created LLM client with tracing", nil)

	// Create memory with tracing
	mem := memory.NewConversationBuffer()
	memWithOTel := tracing.NewMemoryOTelMiddleware(mem, otelTracer)
	logger.Info(ctx, "Created memory with tracing", nil)

	// Create agent
	agent, err := agent.NewAgent(
		agent.WithLLM(llmWithOTel),
		agent.WithMemory(memWithOTel),
		agent.WithSystemPrompt(os.Getenv("SYSTEM_PROMPT")),
	)
	if err != nil {
		logger.Error(ctx, "Failed to create agent", map[string]interface{}{"error": err.Error()})
		return nil, ctx, fmt.Errorf("failed to create agent: %w", err)
	}
	logger.Info(ctx, "Agent created successfully", nil)

	return agent, ctx, nil
}
