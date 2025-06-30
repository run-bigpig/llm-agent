package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/run-bigpig/llm-agent/pkg/interfaces"
	"github.com/run-bigpig/llm-agent/pkg/llm"
	"github.com/run-bigpig/llm-agent/pkg/logging"
	"github.com/run-bigpig/llm-agent/pkg/multitenancy"
	"github.com/run-bigpig/llm-agent/pkg/retry"
	"github.com/sashabaranov/go-openai"
)

// Define a custom type for context keys to avoid collisions
type contextKey string

// Define constants for context keys
const organizationKey contextKey = "organization"

// OpenAIClient implements the LLM interface for OpenAI
type OpenAIClient struct {
	Client        *openai.Client
	Model         string
	logger        logging.Logger
	retryExecutor *retry.Executor
}

// Option represents an option for configuring the OpenAI client
type Option func(*OpenAIClient)

// WithModel sets the model for the OpenAI client
func WithModel(model string) Option {
	return func(c *OpenAIClient) {
		c.Model = model
	}
}

// WithLogger sets the logger for the OpenAI client
func WithLogger(logger logging.Logger) Option {
	return func(c *OpenAIClient) {
		c.logger = logger
	}
}

// WithRetry configures retry policy for the client
func WithRetry(opts ...retry.Option) Option {
	return func(c *OpenAIClient) {
		c.retryExecutor = retry.NewExecutor(retry.NewPolicy(opts...))
	}
}

// NewClient creates a new OpenAI client
func NewClient(apiKey string, options ...Option) *OpenAIClient {
	// Create client with default options
	client := &OpenAIClient{
		Client: openai.NewClient(apiKey),
		Model:  "gpt-4o-mini",
		logger: logging.New(),
	}

	// Apply options
	for _, option := range options {
		option(client)
	}

	return client
}

// Generate generates text from a prompt
func (c *OpenAIClient) Generate(ctx context.Context, prompt string, options ...interfaces.GenerateOption) (string, error) {
	// Apply options
	params := &interfaces.GenerateOptions{
		LLMConfig: &interfaces.LLMConfig{
			Temperature: 0.7,
		},
	}

	for _, option := range options {
		option(params)
	}

	// Get organization ID from context if available
	orgID, _ := multitenancy.GetOrgID(ctx)
	if orgID != "" {
		ctx = context.WithValue(ctx, organizationKey, orgID)
	}

	// Create request with system message if provided
	messages := []openai.ChatCompletionMessage{}

	// Add system message if available
	if params.SystemMessage != "" {
		// If reasoning is enabled, enhance the system message
		if params.LLMConfig != nil && params.LLMConfig.Reasoning != "" {
			switch params.LLMConfig.Reasoning {
			case "minimal":
				params.SystemMessage = fmt.Sprintf("%s\n\nWhen responding, briefly explain your thought process.", params.SystemMessage)
				c.logger.Debug(ctx, "Using minimal reasoning mode", nil)
			case "comprehensive":
				params.SystemMessage = fmt.Sprintf("%s\n\nWhen responding, please think step-by-step and explain your complete reasoning process in detail.", params.SystemMessage)
				c.logger.Debug(ctx, "Using comprehensive reasoning mode", nil)
			case "none":
				params.SystemMessage = fmt.Sprintf("%s\n\nProvide direct, concise answers without explaining your reasoning or showing calculations.", params.SystemMessage)
				c.logger.Debug(ctx, "Using no reasoning mode with explicit instruction", nil)
			default:
				c.logger.Warn(ctx, "Unknown reasoning mode, using default behavior", map[string]interface{}{"reasoning": params.LLMConfig.Reasoning})
			}
		}

		messages = append(messages, openai.ChatCompletionMessage{
			Role:    "system",
			Content: params.SystemMessage,
		})
		c.logger.Debug(ctx, "Using system message", map[string]interface{}{"system_message": params.SystemMessage})
	} else if params.LLMConfig != nil && params.LLMConfig.Reasoning != "" {
		// If no system message but reasoning is enabled, create a system message just for reasoning
		var systemMessage string
		switch params.LLMConfig.Reasoning {
		case "minimal":
			systemMessage = "When responding, briefly explain your thought process."
			c.logger.Debug(ctx, "Using minimal reasoning mode with default system message", nil)
		case "comprehensive":
			systemMessage = "When responding, please think step-by-step and explain your complete reasoning process in detail."
			c.logger.Debug(ctx, "Using comprehensive reasoning mode with default system message", nil)
		case "none":
			systemMessage = "Provide direct, concise answers without explaining your reasoning or showing calculations."
			c.logger.Debug(ctx, "Using no reasoning mode with explicit instruction", nil)
		default:
			c.logger.Warn(ctx, "Unknown reasoning mode, using default behavior", map[string]interface{}{"reasoning": params.LLMConfig.Reasoning})
		}

		if systemMessage != "" {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "system",
				Content: systemMessage,
			})
			c.logger.Debug(ctx, "Using system message for reasoning", map[string]interface{}{"system_message": systemMessage})
		}
	}

	// Add user message
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    "user",
		Content: prompt,
	})

	// Create request
	req := openai.ChatCompletionRequest{
		Model:    c.Model,
		Messages: messages,
	}

	if params.LLMConfig != nil {
		req.Temperature = float32(params.LLMConfig.Temperature)
		req.TopP = float32(params.LLMConfig.TopP)
		req.FrequencyPenalty = float32(params.LLMConfig.FrequencyPenalty)
		req.PresencePenalty = float32(params.LLMConfig.PresencePenalty)
		req.Stop = params.LLMConfig.StopSequences
	}

	// Set response format if provided
	if params.ResponseFormat != nil {
		req.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name:   params.ResponseFormat.Name,
				Schema: params.ResponseFormat.Schema,
			},
		}
		c.logger.Debug(ctx, "Using response format", map[string]interface{}{"format": *params.ResponseFormat})
	}

	// Set organization ID if available
	if orgID, ok := ctx.Value(organizationKey).(string); ok && orgID != "" {
		req.User = orgID
	}

	var resp openai.ChatCompletionResponse
	var err error

	operation := func() error {
		var reasoningMode string
		if params.LLMConfig != nil && params.LLMConfig.Reasoning != "" {
			reasoningMode = params.LLMConfig.Reasoning
		} else {
			reasoningMode = "none"
		}

		c.logger.Debug(ctx, "Executing OpenAI API request", map[string]interface{}{
			"model":             c.Model,
			"temperature":       req.Temperature,
			"top_p":             req.TopP,
			"frequency_penalty": req.FrequencyPenalty,
			"presence_penalty":  req.PresencePenalty,
			"stop_sequences":    req.Stop,
			"messages":          len(req.Messages),
			"response_format":   req.ResponseFormat != nil,
			"reasoning":         reasoningMode,
		})

		resp, err = c.Client.CreateChatCompletion(ctx, req)
		if err != nil {
			c.logger.Error(ctx, "Error from OpenAI API", map[string]interface{}{
				"error": err.Error(),
				"model": c.Model,
			})
			return fmt.Errorf("failed to generate text: %w", err)
		}
		return nil
	}

	if c.retryExecutor != nil {
		c.logger.Debug(ctx, "Using retry mechanism for OpenAI request", map[string]interface{}{
			"model": c.Model,
		})
		err = c.retryExecutor.Execute(ctx, operation)
	} else {
		err = operation()
	}

	if err != nil {
		return "", err
	}

	// Return response
	if len(resp.Choices) > 0 {
		c.logger.Debug(ctx, "Successfully received response from OpenAI", map[string]interface{}{
			"model": c.Model,
		})
		return resp.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("no response from OpenAI API")
}

// Chat uses the ChatCompletion API to have a conversation (messages) with a model
func (c *OpenAIClient) Chat(ctx context.Context, messages []llm.Message, params *llm.GenerateParams) (string, error) {
	if params == nil {
		params = llm.DefaultGenerateParams()
	}

	// Handle reasoning if specified
	var systemMessage string
	var hasSystemMessage bool

	// Check for existing system message and apply reasoning if needed
	for i, msg := range messages {
		if msg.Role == "system" {
			hasSystemMessage = true

			// Apply reasoning to the system message if specified
			if params.Reasoning != "" {
				switch params.Reasoning {
				case "minimal":
					messages[i].Content = fmt.Sprintf("%s\n\nWhen responding, briefly explain your thought process.", msg.Content)
					c.logger.Debug(ctx, "Using minimal reasoning mode", nil)
				case "comprehensive":
					messages[i].Content = fmt.Sprintf("%s\n\nWhen responding, please think step-by-step and explain your complete reasoning process in detail.", msg.Content)
					c.logger.Debug(ctx, "Using comprehensive reasoning mode", nil)
				case "none":
					messages[i].Content = fmt.Sprintf("%s\n\nProvide direct, concise answers without explaining your reasoning or showing calculations.", msg.Content)
					c.logger.Debug(ctx, "Using no reasoning mode with explicit instruction", nil)
				default:
					c.logger.Warn(ctx, "Unknown reasoning mode, using default behavior", map[string]interface{}{
						"reasoning": params.Reasoning,
					})
				}
			}
			break
		}
	}

	// If no system message exists but reasoning is specified, create one
	if !hasSystemMessage && params.Reasoning != "" {
		switch params.Reasoning {
		case "minimal":
			systemMessage = "When responding, briefly explain your thought process."
			c.logger.Debug(ctx, "Using minimal reasoning mode with default system message", nil)
		case "comprehensive":
			systemMessage = "When responding, please think step-by-step and explain your complete reasoning process in detail."
			c.logger.Debug(ctx, "Using comprehensive reasoning mode with default system message", nil)
		case "none":
			systemMessage = "Provide direct, concise answers without explaining your reasoning or showing calculations."
			c.logger.Debug(ctx, "Using no reasoning mode with explicit instruction", nil)
		default:
			c.logger.Warn(ctx, "Unknown reasoning mode, using default behavior", map[string]interface{}{
				"reasoning": params.Reasoning,
			})
		}

		// Add system message if one was created
		if systemMessage != "" {
			messages = append([]llm.Message{{Role: "system", Content: systemMessage}}, messages...)
		}
	}

	// Convert messages to the OpenAI Chat format
	chatMessages := make([]openai.ChatCompletionMessage, len(messages))
	for i, msg := range messages {
		chatMessages[i] = openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	// Create chat request
	req := openai.ChatCompletionRequest{
		Model:            c.Model,
		Messages:         chatMessages,
		Temperature:      float32(params.Temperature),
		TopP:             float32(params.TopP),
		FrequencyPenalty: float32(params.FrequencyPenalty),
		PresencePenalty:  float32(params.PresencePenalty),
		Stop:             params.StopSequences,
	}

	var resp openai.ChatCompletionResponse
	var err error

	operation := func() error {
		c.logger.Debug(ctx, "Executing OpenAI Chat API request", map[string]interface{}{
			"model":             c.Model,
			"temperature":       req.Temperature,
			"top_p":             req.TopP,
			"frequency_penalty": req.FrequencyPenalty,
			"presence_penalty":  req.PresencePenalty,
			"stop_sequences":    req.Stop,
			"messages":          len(req.Messages),
			"reasoning":         params.Reasoning,
		})

		resp, err = c.Client.CreateChatCompletion(ctx, req)
		if err != nil {
			c.logger.Error(ctx, "Error from OpenAI Chat API", map[string]interface{}{
				"error": err.Error(),
				"model": c.Model,
			})
			return fmt.Errorf("failed to create chat completion: %w", err)
		}
		return nil
	}

	if c.retryExecutor != nil {
		c.logger.Debug(ctx, "Using retry mechanism for OpenAI Chat request", map[string]interface{}{
			"model": c.Model,
		})
		err = c.retryExecutor.Execute(ctx, operation)
	} else {
		err = operation()
	}

	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no completions returned")
	}

	c.logger.Debug(ctx, "Successfully received chat response from OpenAI", map[string]interface{}{
		"model": c.Model,
	})

	return resp.Choices[0].Message.Content, nil
}

// GenerateWithTools implements interfaces.LLM.GenerateWithTools
func (c *OpenAIClient) GenerateWithTools(ctx context.Context, prompt string, tools []interfaces.Tool, options ...interfaces.GenerateOption) (string, error) {
	// Convert options to params
	params := &interfaces.GenerateOptions{}
	for _, opt := range options {
		if opt != nil {
			opt(params)
		}
	}

	// Set default values only if they're not provided
	if params.LLMConfig == nil {
		params.LLMConfig = &interfaces.LLMConfig{
			Temperature:      0.7,
			TopP:             1.0,
			FrequencyPenalty: 0.0,
			PresencePenalty:  0.0,
		}
	}

	// Check for organization ID in context
	orgID := "default"
	if id, err := multitenancy.GetOrgID(ctx); err == nil {
		orgID = id
	}
	ctx = context.WithValue(ctx, organizationKey, orgID)

	// Convert tools to OpenAI format
	openaiTools := make([]openai.Tool, len(tools))
	for i, tool := range tools {
		// Convert ParameterSpec to JSON Schema
		properties := make(map[string]interface{})
		required := []string{}

		for name, param := range tool.Parameters() {
			properties[name] = map[string]interface{}{
				"type":        param.Type,
				"description": param.Description,
			}
			if param.Default != nil {
				properties[name].(map[string]interface{})["default"] = param.Default
			}
			if param.Required {
				required = append(required, name)
			}
			if param.Items != nil {
				properties[name].(map[string]interface{})["items"] = map[string]interface{}{
					"type": param.Items.Type,
				}
				if param.Items.Enum != nil {
					properties[name].(map[string]interface{})["items"].(map[string]interface{})["enum"] = param.Items.Enum
				}
			}
			if param.Enum != nil {
				properties[name].(map[string]interface{})["enum"] = param.Enum
			}
		}

		openaiTools[i] = openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": properties,
					"required":   required,
				},
			},
		}
	}

	// Create messages array with system message if provided
	messages := []openai.ChatCompletionMessage{}

	// Add system message if available
	if params.SystemMessage != "" {
		// If reasoning is enabled, enhance the system message
		if params.LLMConfig != nil && params.LLMConfig.Reasoning != "" {
			switch params.LLMConfig.Reasoning {
			case "minimal":
				params.SystemMessage = fmt.Sprintf("%s\n\nWhen responding, briefly explain your thought process.", params.SystemMessage)
				c.logger.Debug(ctx, "Using minimal reasoning mode", nil)
			case "comprehensive":
				params.SystemMessage = fmt.Sprintf("%s\n\nWhen responding, please think step-by-step and explain your complete reasoning process in detail.", params.SystemMessage)
				c.logger.Debug(ctx, "Using comprehensive reasoning mode", nil)
			case "none":
				params.SystemMessage = fmt.Sprintf("%s\n\nProvide direct, concise answers without explaining your reasoning or showing calculations.", params.SystemMessage)
				c.logger.Debug(ctx, "Using no reasoning mode with explicit instruction", nil)
			default:
				c.logger.Warn(ctx, "Unknown reasoning mode, using default behavior", map[string]interface{}{"reasoning": params.LLMConfig.Reasoning})
			}
		}

		messages = append(messages, openai.ChatCompletionMessage{
			Role:    "system",
			Content: params.SystemMessage,
		})
		c.logger.Debug(ctx, "Using system message", map[string]interface{}{"system_message": params.SystemMessage})
	} else if params.LLMConfig != nil && params.LLMConfig.Reasoning != "" {
		// If no system message but reasoning is enabled, create a system message just for reasoning
		var systemMessage string
		switch params.LLMConfig.Reasoning {
		case "minimal":
			systemMessage = "When responding, briefly explain your thought process."
			c.logger.Debug(ctx, "Using minimal reasoning mode with default system message", nil)
		case "comprehensive":
			systemMessage = "When responding, please think step-by-step and explain your complete reasoning process in detail."
			c.logger.Debug(ctx, "Using comprehensive reasoning mode with default system message", nil)
		case "none":
			// No system message needed
			c.logger.Debug(ctx, "Using no reasoning mode", nil)
		default:
			c.logger.Warn(ctx, "Unknown reasoning mode, using default behavior", map[string]interface{}{"reasoning": params.LLMConfig.Reasoning})
		}

		if systemMessage != "" {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    "system",
				Content: systemMessage,
			})
			c.logger.Debug(ctx, "Using system message for reasoning", map[string]interface{}{"system_message": systemMessage})
		}
	}

	// Add user message
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    "user",
		Content: prompt,
	})

	req := openai.ChatCompletionRequest{
		Model:             c.Model,
		Messages:          messages,
		Tools:             openaiTools,
		Temperature:       float32(params.LLMConfig.Temperature),
		TopP:              float32(params.LLMConfig.TopP),
		FrequencyPenalty:  float32(params.LLMConfig.FrequencyPenalty),
		PresencePenalty:   float32(params.LLMConfig.PresencePenalty),
		Stop:              params.LLMConfig.StopSequences,
		ParallelToolCalls: true,
	}

	// Set response format if provided
	if params.ResponseFormat != nil {
		req.ResponseFormat = &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
				Name:   params.ResponseFormat.Name,
				Schema: params.ResponseFormat.Schema,
			},
		}
		c.logger.Debug(ctx, "Using response format", map[string]interface{}{"format": *params.ResponseFormat})
	}

	// Send request
	var reasoningMode string
	if params.LLMConfig != nil && params.LLMConfig.Reasoning != "" {
		reasoningMode = params.LLMConfig.Reasoning
	} else {
		reasoningMode = "none"
	}

	c.logger.Debug(ctx, "Sending request with tools to OpenAI", map[string]interface{}{
		"model":             c.Model,
		"temperature":       req.Temperature,
		"top_p":             req.TopP,
		"frequency_penalty": req.FrequencyPenalty,
		"presence_penalty":  req.PresencePenalty,
		"stop_sequences":    req.Stop,
		"messages":          len(req.Messages),
		"tools":             len(req.Tools),
		"response_format":   req.ResponseFormat != nil,
		"parallel_tools":    req.ParallelToolCalls,
		"reasoning":         reasoningMode,
	})
	resp, err := c.Client.CreateChatCompletion(ctx, req)
	if err != nil {
		c.logger.Error(ctx, "Error from OpenAI API", map[string]interface{}{"error": err.Error()})
		return "", fmt.Errorf("failed to create chat completion: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no completions returned")
	}

	// Check if the model wants to use tools
	if len(resp.Choices[0].Message.ToolCalls) > 0 {
		// The model wants to use tools
		toolCalls := resp.Choices[0].Message.ToolCalls
		c.logger.Info(ctx, "Processing tool calls", map[string]interface{}{"count": len(toolCalls)})

		// Replace multi_tool_use.parallel name if present
		for i := range toolCalls {
			if toolCalls[i].Function.Name == "multi_tool_use.parallel" {
				c.logger.Info(ctx, "Replacing multi_tool_use.parallel with parallel_tool_use", nil)
				// it's required because the function name must match ^[a-zA-Z0-9_-]+$ when sending the request to OpenAI
				toolCalls[i].Function.Name = "parallel_tool_use"
			}
		}

		// Create a new conversation with the initial messages
		messages := []openai.ChatCompletionMessage{
			{Role: "user", Content: prompt},
			resp.Choices[0].Message,
		}

		// Process each tool call
		for _, toolCall := range toolCalls {

			if toolCall.Function.Name == "parallel_tool_use" {
				c.logger.Info(ctx, "Parallel tool call", map[string]interface{}{"toolName": toolCall.Function.Name})

				arguments := toolCall.Function.Arguments
				var toolUsesWrapper struct {
					ToolUses []map[string]interface{} `json:"tool_uses"`
				}
				err := json.Unmarshal([]byte(arguments), &toolUsesWrapper)
				if err != nil {
					c.logger.Error(ctx, "Error unmarshalling tool uses", map[string]interface{}{"error": err.Error()})
					continue
				}

				type toolResult struct {
					index  int
					result string
					err    error
				}

				resultCh := make(chan toolResult, len(toolUsesWrapper.ToolUses))
				var wg sync.WaitGroup

				// Launch goroutines for concurrent tool execution
				for i, toolUse := range toolUsesWrapper.ToolUses {
					wg.Add(1)
					go func(index int, toolUse map[string]interface{}) {
						defer wg.Done()

						toolName := toolUse["recipient_name"].(string)
						parameters := toolUse["parameters"].(map[string]interface{})

						c.logger.Info(ctx, "Parallel tool use", map[string]interface{}{"toolName": toolName, "parameters": parameters})

						// Convert parameters to JSON string
						paramsBytes, err := json.Marshal(parameters)
						if err != nil {
							c.logger.Error(ctx, "Error marshalling parameters", map[string]interface{}{"error": err.Error()})
							resultCh <- toolResult{index: index, result: "", err: err}
							return
						}

						// Find the correct tool for this operation
						var tool interfaces.Tool
						for _, t := range tools {
							if t.Name() == toolName {
								tool = t
								break
							}
						}

						if tool == nil {
							err := fmt.Errorf("tool not found: %s", toolName)
							c.logger.Error(ctx, "Tool not found in parallel execution", map[string]interface{}{"toolName": toolName})
							resultCh <- toolResult{index: index, result: "", err: err}
							return
						}

						c.logger.Info(ctx, "Executing tool", map[string]interface{}{"toolName": toolName, "parameters": string(paramsBytes)})

						result, err := tool.Execute(ctx, string(paramsBytes))
						resultCh <- toolResult{index: index, result: result, err: err}
					}(i, toolUse)
				}

				// Close channel when all goroutines complete
				go func() {
					wg.Wait()
					close(resultCh)
				}()

				// Collect results and check for errors
				toolsResults := make([]string, len(toolUsesWrapper.ToolUses))
				for result := range resultCh {
					if result.err != nil {
						c.logger.Error(ctx, "Error executing tool", map[string]interface{}{"error": result.err.Error()})
						return "", fmt.Errorf("error executing tool: %s", result.err.Error())
					}
					toolsResults[result.index] = result.result
				}

				messages = append(messages, openai.ChatCompletionMessage{
					Role:       "tool",
					Content:    strings.Join(toolsResults, "\n"),
					ToolCallID: toolCall.ID,
					Name:       "parallel_tool_use",
				})
				continue
			}

			// Find the requested tool
			var selectedTool interfaces.Tool
			for _, tool := range tools {
				if tool.Name() == toolCall.Function.Name {
					selectedTool = tool
					break
				}
			}

			if selectedTool == nil || selectedTool.Name() == "" {
				c.logger.Error(ctx, "Tool not found", map[string]interface{}{
					"toolName": toolCall.Function.Name,
					"toolcall": toolCall,
					"resp":     resp,
				})
				return "", fmt.Errorf("tool not found: %s", toolCall.Function.Name)
			}

			// Execute the tool
			c.logger.Info(ctx, "Executing tool", map[string]interface{}{"toolName": selectedTool.Name()})
			toolResult, err := selectedTool.Execute(ctx, toolCall.Function.Arguments)
			if err != nil {
				c.logger.Error(ctx, "Error executing tool", map[string]interface{}{"toolName": selectedTool.Name(), "error": err.Error()})
				// Add error message as tool response
				messages = append(messages, openai.ChatCompletionMessage{
					Role:       "tool",
					Content:    fmt.Sprintf("Error: %v", err),
					Name:       selectedTool.Name(),
					ToolCallID: toolCall.ID,
				})
				continue
			}

			// Add tool result to messages
			messages = append(messages, openai.ChatCompletionMessage{
				Role:       "tool",
				Content:    toolResult,
				Name:       selectedTool.Name(),
				ToolCallID: toolCall.ID,
			})
		}

		// Get the final response
		var reasoningMode string
		if params.LLMConfig != nil && params.LLMConfig.Reasoning != "" {
			reasoningMode = params.LLMConfig.Reasoning
		} else {
			reasoningMode = "none"
		}

		c.logger.Info(ctx, "Sending final request with tool results", map[string]interface{}{
			"model":             c.Model,
			"temperature":       req.Temperature,
			"top_p":             req.TopP,
			"frequency_penalty": req.FrequencyPenalty,
			"presence_penalty":  req.PresencePenalty,
			"stop_sequences":    req.Stop,
			"messages":          len(messages),
			"reasoning":         reasoningMode,
		})

		req := openai.ChatCompletionRequest{
			Model:            c.Model,
			Messages:         messages,
			Temperature:      float32(params.LLMConfig.Temperature),
			TopP:             float32(params.LLMConfig.TopP),
			FrequencyPenalty: float32(params.LLMConfig.FrequencyPenalty),
			PresencePenalty:  float32(params.LLMConfig.PresencePenalty),
			Stop:             params.LLMConfig.StopSequences,
		}

		// Set response format for final request if provided
		if params.ResponseFormat != nil {
			req.ResponseFormat = &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONSchema,
				JSONSchema: &openai.ChatCompletionResponseFormatJSONSchema{
					Name:   params.ResponseFormat.Name,
					Schema: params.ResponseFormat.Schema,
				},
			}
			c.logger.Debug(ctx, "Using response format", map[string]interface{}{"format": *params.ResponseFormat})
		}

		finalCompletion, err := c.Client.CreateChatCompletion(ctx, req)
		if err != nil {
			c.logger.Error(ctx, "Error from final OpenAI API call", map[string]interface{}{
				"error": err.Error(),
			})
			return "", fmt.Errorf("failed to create final chat completion: %w", err)
		}

		if len(finalCompletion.Choices) == 0 {
			return "", fmt.Errorf("no completions returned")
		}

		content := strings.TrimSpace(finalCompletion.Choices[0].Message.Content)
		return content, nil
	}

	// No tool was used, return the direct response
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	return content, nil
}

// Name implements interfaces.LLM.Name
func (c *OpenAIClient) Name() string {
	return "openai"
}

// WithTemperature creates a GenerateOption to set the temperature
func WithTemperature(temperature float64) interfaces.GenerateOption {
	return func(options *interfaces.GenerateOptions) {
		options.LLMConfig.Temperature = temperature
	}
}

// WithTopP creates a GenerateOption to set the top_p
func WithTopP(topP float64) interfaces.GenerateOption {
	return func(options *interfaces.GenerateOptions) {
		options.LLMConfig.TopP = topP
	}
}

// WithFrequencyPenalty creates a GenerateOption to set the frequency penalty
func WithFrequencyPenalty(frequencyPenalty float64) interfaces.GenerateOption {
	return func(options *interfaces.GenerateOptions) {
		options.LLMConfig.FrequencyPenalty = frequencyPenalty
	}
}

// WithPresencePenalty creates a GenerateOption to set the presence penalty
func WithPresencePenalty(presencePenalty float64) interfaces.GenerateOption {
	return func(options *interfaces.GenerateOptions) {
		options.LLMConfig.PresencePenalty = presencePenalty
	}
}

// WithStopSequences creates a GenerateOption to set the stop sequences
func WithStopSequences(stopSequences []string) interfaces.GenerateOption {
	return func(options *interfaces.GenerateOptions) {
		options.LLMConfig.StopSequences = stopSequences
	}
}

// WithSystemMessage creates a GenerateOption to set the system message
func WithSystemMessage(systemMessage string) interfaces.GenerateOption {
	return func(options *interfaces.GenerateOptions) {
		options.SystemMessage = systemMessage
	}
}

// WithResponseFormat creates a GenerateOption to set the response format
func WithResponseFormat(format interfaces.ResponseFormat) interfaces.GenerateOption {
	return func(options *interfaces.GenerateOptions) {
		options.ResponseFormat = &format
	}
}

// WithReasoning creates a GenerateOption to set the reasoning mode
// reasoning can be "none" (direct answers), "minimal" (brief explanations),
// or "comprehensive" (detailed step-by-step reasoning)
func WithReasoning(reasoning string) interfaces.GenerateOption {
	return func(options *interfaces.GenerateOptions) {
		if options.LLMConfig == nil {
			options.LLMConfig = &interfaces.LLMConfig{}
		}
		options.LLMConfig.Reasoning = reasoning
	}
}
