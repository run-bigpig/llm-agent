package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/run-bigpig/llm-agent/pkg/executionplan"
	"github.com/run-bigpig/llm-agent/pkg/interfaces"
	"github.com/run-bigpig/llm-agent/pkg/llm/openai"
	"github.com/run-bigpig/llm-agent/pkg/mcp"
	"github.com/run-bigpig/llm-agent/pkg/multitenancy"
)

// Agent represents an AI agent
type Agent struct {
	llm                  interfaces.LLM
	memory               interfaces.Memory
	tools                []interfaces.Tool
	orgID                string
	tracer               interfaces.Tracer
	guardrails           interfaces.Guardrails
	systemPrompt         string
	name                 string                   // Name of the agent, e.g., "PlatformOps", "Math", "Research"
	requirePlanApproval  bool                     // New field to control whether execution plans require approval
	planStore            *executionplan.Store     // Store for execution plans
	planGenerator        *executionplan.Generator // Generator for execution plans
	planExecutor         *executionplan.Executor  // Executor for execution plans
	generatedAgentConfig *AgentConfig
	generatedTaskConfigs TaskConfigs
	responseFormat       *interfaces.ResponseFormat // Response format for the agent
	llmConfig            *interfaces.LLMConfig
	mcpServers           []interfaces.MCPServer // MCP servers for the agent
}

// Option represents an option for configuring an agent
type Option func(*Agent)

// WithLLM sets the LLM for the agent
func WithLLM(llm interfaces.LLM) Option {
	return func(a *Agent) {
		a.llm = llm
	}
}

// WithMemory sets the memory for the agent
func WithMemory(memory interfaces.Memory) Option {
	return func(a *Agent) {
		a.memory = memory
	}
}

// WithTools sets the tools for the agent
func WithTools(tools ...interfaces.Tool) Option {
	return func(a *Agent) {
		a.tools = tools
	}
}

// WithOrgID sets the organization ID for multi-tenancy
func WithOrgID(orgID string) Option {
	return func(a *Agent) {
		a.orgID = orgID
	}
}

// WithTracer sets the tracer for the agent
func WithTracer(tracer interfaces.Tracer) Option {
	return func(a *Agent) {
		a.tracer = tracer
	}
}

// WithGuardrails sets the guardrails for the agent
func WithGuardrails(guardrails interfaces.Guardrails) Option {
	return func(a *Agent) {
		a.guardrails = guardrails
	}
}

// WithSystemPrompt sets the system prompt for the agent
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) {
		a.systemPrompt = prompt
	}
}

// WithRequirePlanApproval sets whether execution plans require user approval
func WithRequirePlanApproval(require bool) Option {
	return func(a *Agent) {
		a.requirePlanApproval = require
	}
}

// WithName sets the name for the agent
func WithName(name string) Option {
	return func(a *Agent) {
		a.name = name
	}
}

// WithAgentConfig sets the agent configuration from a YAML config
func WithAgentConfig(config AgentConfig, variables map[string]string) Option {
	return func(a *Agent) {
		systemPrompt := FormatSystemPromptFromConfig(config, variables)
		a.systemPrompt = systemPrompt
	}
}

// WithResponseFormat sets the response format for the agent
func WithResponseFormat(formatType interfaces.ResponseFormat) Option {
	return func(a *Agent) {
		a.responseFormat = &formatType
	}
}

func WithLLMConfig(config interfaces.LLMConfig) Option {
	return func(a *Agent) {
		a.llmConfig = &config
	}
}

// WithMCPServers sets the MCP servers for the agent
func WithMCPServers(mcpServers []interfaces.MCPServer) Option {
	return func(a *Agent) {
		a.mcpServers = mcpServers
	}
}

// NewAgent creates a new agent with the given options
func NewAgent(options ...Option) (*Agent, error) {
	agent := &Agent{
		requirePlanApproval: true, // Default to requiring approval
	}

	for _, option := range options {
		option(agent)
	}

	// Validate required fields
	if agent.llm == nil {
		return nil, fmt.Errorf("LLM is required")
	}

	// Initialize execution plan components
	agent.planStore = executionplan.NewStore()
	agent.planGenerator = executionplan.NewGenerator(agent.llm, agent.tools, agent.systemPrompt)
	agent.planExecutor = executionplan.NewExecutor(agent.tools)

	return agent, nil
}

// NewAgentWithAutoConfig creates a new agent with automatic configuration generation
// based on the system prompt if explicit configuration is not provided
func NewAgentWithAutoConfig(ctx context.Context, options ...Option) (*Agent, error) {
	// First create an agent with the provided options
	agent, err := NewAgent(options...)
	if err != nil {
		return nil, err
	}

	// If the agent doesn't have a name, set a default one
	if agent.name == "" {
		agent.name = "Auto-Configured Agent"
	}

	// If the system prompt is provided but no configuration was explicitly set,
	// generate configuration using the LLM
	if agent.systemPrompt != "" {
		// Generate agent and task configurations from the system prompt
		agentConfig, taskConfigs, err := GenerateConfigFromSystemPrompt(ctx, agent.llm, agent.systemPrompt)
		if err != nil {
			// If we fail to generate configs, just continue with the manual system prompt
			// We don't want to fail agent creation just because auto-config failed
			return agent, nil
		}

		// Create a task configuration map
		taskConfigMap := make(TaskConfigs)
		for i, taskConfig := range taskConfigs {
			taskName := fmt.Sprintf("auto_task_%d", i+1)
			taskConfig.Agent = agent.name // Set the task to use this agent
			taskConfigMap[taskName] = taskConfig
		}

		// Store generated configurations in agent so they can be accessed later
		agent.generatedAgentConfig = &agentConfig
		agent.generatedTaskConfigs = taskConfigMap
	}

	return agent, nil
}

// NewAgentFromConfig creates a new agent from a YAML configuration
func NewAgentFromConfig(agentName string, configs AgentConfigs, variables map[string]string, options ...Option) (*Agent, error) {
	config, exists := configs[agentName]
	if !exists {
		return nil, fmt.Errorf("agent configuration for %s not found", agentName)
	}

	// Add the agent config option
	configOption := WithAgentConfig(config, variables)
	nameOption := WithName(agentName)

	// Combine all options
	allOptions := append([]Option{configOption, nameOption}, options...)

	return NewAgent(allOptions...)
}

// CreateAgentForTask creates a new agent for a specific task
func CreateAgentForTask(taskName string, agentConfigs AgentConfigs, taskConfigs TaskConfigs, variables map[string]string, options ...Option) (*Agent, error) {
	agentName, err := GetAgentForTask(taskConfigs, taskName)
	if err != nil {
		return nil, err
	}

	return NewAgentFromConfig(agentName, agentConfigs, variables, options...)
}

// Run runs the agent with the given input
func (a *Agent) Run(ctx context.Context, input string) (string, error) {
	// If orgID is set on the agent, add it to the context
	if a.orgID != "" {
		ctx = multitenancy.WithOrgID(ctx, a.orgID)
	}

	// Start tracing if available
	var span interfaces.Span
	if a.tracer != nil {
		ctx, span = a.tracer.StartSpan(ctx, "agent.Run")
		defer span.End()
	}

	// Add user message to memory
	if a.memory != nil {
		if err := a.memory.AddMessage(ctx, interfaces.Message{
			Role:    "user",
			Content: input,
		}); err != nil {
			return "", fmt.Errorf("failed to add user message to memory: %w", err)
		}
	}

	// Apply guardrails to input if available
	if a.guardrails != nil {
		guardedInput, err := a.guardrails.ProcessInput(ctx, input)
		if err != nil {
			return "", fmt.Errorf("guardrails error: %w", err)
		}
		input = guardedInput
	}

	// Check if the input is related to an existing plan
	taskID, action, planInput := a.extractPlanAction(input)
	if taskID != "" {
		return a.handlePlanAction(ctx, taskID, action, planInput)
	}

	// Check if the user is asking about the agent's role or identity
	if a.systemPrompt != "" && a.isAskingAboutRole(input) {
		response := a.generateRoleResponse()

		// Add the role response to memory if available
		if a.memory != nil {
			if err := a.memory.AddMessage(ctx, interfaces.Message{
				Role:    "assistant",
				Content: response,
			}); err != nil {
				return "", fmt.Errorf("failed to add role response to memory: %w", err)
			}
		}

		return response, nil
	}

	allTools := a.tools

	// Add MCP tools if available
	if len(a.mcpServers) > 0 {
		mcpTools, err := a.collectMCPTools(ctx)
		if err != nil {
			// Log the error but continue with the agent tools
			fmt.Printf("Failed to collect MCP tools: %v\n", err)
		} else if len(mcpTools) > 0 {
			allTools = append(allTools, mcpTools...)
		}
	}
	// If tools are available and plan approval is required, generate an execution plan
	if (len(allTools) > 0) && a.requirePlanApproval {
		a.planGenerator = executionplan.NewGenerator(a.llm, allTools, a.systemPrompt)
		return a.runWithExecutionPlan(ctx, input)
	}

	// Otherwise, run without an execution plan
	return a.runWithoutExecutionPlanWithTools(ctx, input, allTools)
}

// collectMCPTools collects tools from all MCP servers
func (a *Agent) collectMCPTools(ctx context.Context) ([]interfaces.Tool, error) {
	var mcpTools []interfaces.Tool

	for _, server := range a.mcpServers {
		// List tools from this server
		tools, err := server.ListTools(ctx)
		if err != nil {
			fmt.Printf("Failed to list tools from MCP server: %v\n", err)
			continue
		}

		// Convert MCP tools to agent tools
		for _, mcpTool := range tools {
			// Create a new MCPTool
			tool := mcp.NewMCPTool(mcpTool.Name, mcpTool.Description, mcpTool.Schema, server)
			mcpTools = append(mcpTools, tool)
		}
	}

	return mcpTools, nil
}

// runWithoutExecutionPlanWithTools runs the agent without an execution plan but with the specified tools
func (a *Agent) runWithoutExecutionPlanWithTools(ctx context.Context, input string, tools []interfaces.Tool) (string, error) {
	// Get conversation history if memory is available
	var prompt string
	if a.memory != nil {
		history, err := a.memory.GetMessages(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get conversation history: %w", err)
		}

		// Format history into prompt
		prompt = formatHistoryIntoPrompt(history)
	} else {
		prompt = input
	}

	// Generate response with tools if available
	var response string
	var err error

	// Add system prompt as a generate option
	generateOptions := []interfaces.GenerateOption{}
	if a.systemPrompt != "" {
		generateOptions = append(generateOptions, openai.WithSystemMessage(a.systemPrompt))
	}

	// Add response format as a generate option if available
	if a.responseFormat != nil {
		generateOptions = append(generateOptions, openai.WithResponseFormat(*a.responseFormat))
	}

	if a.llmConfig != nil {
		generateOptions = append(generateOptions, func(options *interfaces.GenerateOptions) {
			options.LLMConfig = a.llmConfig
		})
	}

	if len(tools) > 0 {
		response, err = a.llm.GenerateWithTools(ctx, prompt, tools, generateOptions...)
	} else {
		response, err = a.llm.Generate(ctx, prompt, generateOptions...)
	}

	if err != nil {
		return "", fmt.Errorf("failed to generate response: %w", err)
	}

	// Apply guardrails to output if available
	if a.guardrails != nil {
		guardedResponse, err := a.guardrails.ProcessOutput(ctx, response)
		if err != nil {
			return "", fmt.Errorf("guardrails error: %w", err)
		}
		response = guardedResponse
	}

	// Add agent message to memory
	if a.memory != nil {
		if err := a.memory.AddMessage(ctx, interfaces.Message{
			Role:    "assistant",
			Content: response,
		}); err != nil {
			return "", fmt.Errorf("failed to add agent message to memory: %w", err)
		}
	}

	return response, nil
}

// extractPlanAction attempts to extract a plan action from the user input
// Returns taskID, action, and remaining input
func (a *Agent) extractPlanAction(input string) (string, string, string) {
	// This is a placeholder implementation
	// In a real implementation, you would use NLP or pattern matching to extract plan actions
	return "", "", input
}

// handlePlanAction handles actions related to an existing plan
func (a *Agent) handlePlanAction(ctx context.Context, taskID, action, input string) (string, error) {
	plan, exists := a.planStore.GetPlanByTaskID(taskID)
	if !exists {
		return "", fmt.Errorf("plan with task ID %s not found", taskID)
	}

	switch action {
	case "approve":
		return a.approvePlan(ctx, plan)
	case "modify":
		return a.modifyPlan(ctx, plan, input)
	case "cancel":
		return a.cancelPlan(plan)
	case "status":
		return a.getPlanStatus(plan)
	default:
		return "", fmt.Errorf("unknown plan action: %s", action)
	}
}

// approvePlan approves and executes a plan
func (a *Agent) approvePlan(ctx context.Context, plan *executionplan.ExecutionPlan) (string, error) {
	plan.UserApproved = true
	plan.Status = executionplan.StatusApproved

	// Add the approval to memory
	if a.memory != nil {
		if err := a.memory.AddMessage(ctx, interfaces.Message{
			Role:    "user",
			Content: "I approve the plan. Please proceed with execution.",
		}); err != nil {
			return "", fmt.Errorf("failed to add approval to memory: %w", err)
		}
	}

	// Execute the plan
	result, err := a.planExecutor.ExecutePlan(ctx, plan)
	if err != nil {
		return "", fmt.Errorf("failed to execute plan: %w", err)
	}

	// Add the execution result to memory
	if a.memory != nil {
		if err := a.memory.AddMessage(ctx, interfaces.Message{
			Role:    "assistant",
			Content: result,
		}); err != nil {
			return "", fmt.Errorf("failed to add execution result to memory: %w", err)
		}
	}

	return result, nil
}

// modifyPlan modifies a plan based on user input
func (a *Agent) modifyPlan(ctx context.Context, plan *executionplan.ExecutionPlan, input string) (string, error) {
	// Add the modification request to memory
	if a.memory != nil {
		if err := a.memory.AddMessage(ctx, interfaces.Message{
			Role:    "user",
			Content: "I'd like to modify the plan: " + input,
		}); err != nil {
			return "", fmt.Errorf("failed to add modification request to memory: %w", err)
		}
	}

	// Modify the plan
	modifiedPlan, err := a.planGenerator.ModifyExecutionPlan(ctx, plan, input)
	if err != nil {
		return "", fmt.Errorf("failed to modify plan: %w", err)
	}

	// Update the plan in the store
	a.planStore.StorePlan(modifiedPlan)

	// Format the modified plan
	formattedPlan := executionplan.FormatExecutionPlan(modifiedPlan)

	// Add the modified plan to memory
	if a.memory != nil {
		if err := a.memory.AddMessage(ctx, interfaces.Message{
			Role:    "assistant",
			Content: "I've updated the execution plan based on your feedback:\n\n" + formattedPlan + "\nDo you approve this plan? You can modify it further if needed.",
		}); err != nil {
			return "", fmt.Errorf("failed to add modified plan to memory: %w", err)
		}
	}

	return "I've updated the execution plan based on your feedback:\n\n" + formattedPlan + "\nDo you approve this plan? You can modify it further if needed.", nil
}

// cancelPlan cancels a plan
func (a *Agent) cancelPlan(plan *executionplan.ExecutionPlan) (string, error) {
	a.planExecutor.CancelPlan(plan)

	return "Plan cancelled. What would you like to do instead?", nil
}

// getPlanStatus returns the status of a plan
func (a *Agent) getPlanStatus(plan *executionplan.ExecutionPlan) (string, error) {
	status := a.planExecutor.GetPlanStatus(plan)
	formattedPlan := executionplan.FormatExecutionPlan(plan)

	return fmt.Sprintf("Current plan status: %s\n\n%s", status, formattedPlan), nil
}

// runWithExecutionPlan runs the agent with an execution plan
func (a *Agent) runWithExecutionPlan(ctx context.Context, input string) (string, error) {
	// Generate an execution plan
	plan, err := a.planGenerator.GenerateExecutionPlan(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to generate execution plan: %w", err)
	}

	// Store the plan
	a.planStore.StorePlan(plan)

	// Format the plan for display
	formattedPlan := executionplan.FormatExecutionPlan(plan)

	// Add the plan to memory
	if a.memory != nil {
		if err := a.memory.AddMessage(ctx, interfaces.Message{
			Role:    "assistant",
			Content: "I've created an execution plan for your request:\n\n" + formattedPlan + "\nDo you approve this plan? You can modify it if needed.",
		}); err != nil {
			return "", fmt.Errorf("failed to add plan to memory: %w", err)
		}
	}

	// Return the plan for user approval
	return "I've created an execution plan for your request:\n\n" + formattedPlan + "\nDo you approve this plan? You can modify it if needed.", nil
}

// formatHistoryIntoPrompt formats conversation history into a prompt
func formatHistoryIntoPrompt(history []interfaces.Message) string {
	// Implementation depends on the LLM's expected format
	var prompt string

	// Simple implementation that concatenates messages
	for _, msg := range history {
		role := msg.Role
		content := msg.Content

		prompt += role + ": " + content + "\n"
	}

	return prompt
}

// ApproveExecutionPlan approves an execution plan for execution
func (a *Agent) ApproveExecutionPlan(ctx context.Context, plan *executionplan.ExecutionPlan) (string, error) {
	return a.approvePlan(ctx, plan)
}

// ModifyExecutionPlan modifies an execution plan based on user input
func (a *Agent) ModifyExecutionPlan(ctx context.Context, plan *executionplan.ExecutionPlan, modifications string) (*executionplan.ExecutionPlan, error) {
	return a.planGenerator.ModifyExecutionPlan(ctx, plan, modifications)
}

// GenerateExecutionPlan generates an execution plan
func (a *Agent) GenerateExecutionPlan(ctx context.Context, input string) (*executionplan.ExecutionPlan, error) {
	return a.planGenerator.GenerateExecutionPlan(ctx, input)
}

// isAskingAboutRole determines if the user is asking about the agent's role or identity
func (a *Agent) isAskingAboutRole(input string) bool {
	// Convert input to lowercase for case-insensitive matching
	lowerInput := strings.ToLower(input)

	// Common phrases that indicate a user asking about the agent's role
	roleQueries := []string{
		"what are you",
		"who are you",
		"what is your role",
		"what do you do",
		"what can you do",
		"what is your purpose",
		"what is your function",
		"tell me about yourself",
		"introduce yourself",
		"what are your capabilities",
		"what are you designed to do",
		"what's your job",
		"what kind of assistant are you",
		"your role",
		"your expertise",
		"what are you expert in",
		"what are you specialized in",
		"your specialty",
		"what's your specialty",
	}

	// Check if any of the role query phrases are in the input
	for _, query := range roleQueries {
		if strings.Contains(lowerInput, query) {
			return true
		}
	}

	return false
}

// generateRoleResponse creates a response based on the agent's system prompt
func (a *Agent) generateRoleResponse() string {
	// If the prompt is empty, return a generic response
	if a.systemPrompt == "" || a.llm == nil {
		return "I'm an AI assistant designed to help you with various tasks and answer your questions. How can I assist you today?"
	}

	// Create a prompt that asks the LLM to generate a role description based on the system prompt
	agentName := "an AI assistant"
	if a.name != "" {
		agentName = a.name
	}

	prompt := fmt.Sprintf(`Based on the following system prompt that defines your role and capabilities,
generate a brief, natural-sounding response (3-5 sentences) introducing yourself to a user who asked what you can do.
You are named "%s".
Do not directly quote from the system prompt, but create a conversational first-person response that captures your
purpose, expertise, and how you can help. The response should feel like a natural conversation, not like reading documentation.

System prompt:
%s

Your response should:
1. Introduce yourself using first-person perspective, mentioning your name ("%s")
2. Briefly explain your specialization or purpose
3. Mention 2-3 key areas you can help with
4. End with a friendly question about how you can assist the user

Response:`, agentName, a.systemPrompt, agentName)

	// Generate a response using the LLM with the system prompt as context
	generateOptions := []interfaces.GenerateOption{}

	// Use the same system prompt to ensure consistent persona
	generateOptions = append(generateOptions, openai.WithSystemMessage(a.systemPrompt))

	// Generate the response
	response, err := a.llm.Generate(context.Background(), prompt, generateOptions...)
	if err != nil {
		// Fallback to a simple response in case of errors
		if a.name != "" {
			return fmt.Sprintf("I'm %s, an AI assistant based on the role defined in my system prompt. How can I help you today?", a.name)
		}
		return "I'm an AI assistant based on the role defined in my system prompt. How can I help you today?"
	}

	return response
}

// ExecuteTaskFromConfig executes a task using its YAML configuration
func (a *Agent) ExecuteTaskFromConfig(ctx context.Context, taskName string, taskConfigs TaskConfigs, variables map[string]string) (string, error) {
	taskConfig, exists := taskConfigs[taskName]
	if !exists {
		return "", fmt.Errorf("task configuration for %s not found", taskName)
	}

	// Replace variables in the task description
	description := taskConfig.Description
	for key, value := range variables {
		placeholder := fmt.Sprintf("{%s}", key)
		description = strings.ReplaceAll(description, placeholder, value)
	}

	// Run the agent with the task description
	result, err := a.Run(ctx, description)
	if err != nil {
		return "", fmt.Errorf("failed to execute task %s: %w", taskName, err)
	}

	// If an output file is specified, write the result to the file
	if taskConfig.OutputFile != "" {
		outputPath := taskConfig.OutputFile
		for key, value := range variables {
			placeholder := fmt.Sprintf("{%s}", key)
			outputPath = strings.ReplaceAll(outputPath, placeholder, value)
		}

		err := os.WriteFile(outputPath, []byte(result), 0600)
		if err != nil {
			return result, fmt.Errorf("failed to write output to file %s: %w", outputPath, err)
		}
	}

	return result, nil
}

// GetGeneratedAgentConfig returns the automatically generated agent configuration, if any
func (a *Agent) GetGeneratedAgentConfig() *AgentConfig {
	return a.generatedAgentConfig
}

// GetGeneratedTaskConfigs returns the automatically generated task configurations, if any
func (a *Agent) GetGeneratedTaskConfigs() TaskConfigs {
	return a.generatedTaskConfigs
}

// GetTaskByID returns a task by its ID
func (a *Agent) GetTaskByID(taskID string) (*executionplan.ExecutionPlan, bool) {
	return a.planStore.GetPlanByTaskID(taskID)
}

// ListTasks returns a list of all tasks
func (a *Agent) ListTasks() []*executionplan.ExecutionPlan {
	return a.planStore.ListPlans()
}
