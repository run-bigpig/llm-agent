package interfaces

import "context"

// Tracer represents a tracing system for observability
type Tracer interface {
	// StartSpan starts a new span and returns a new context containing the span
	StartSpan(ctx context.Context, name string) (context.Context, Span)
}

// Span represents a span in a trace
type Span interface {
	// End ends the span
	End()

	// AddEvent adds an event to the span
	AddEvent(name string, attributes map[string]interface{})

	// SetAttribute sets an attribute on the span
	SetAttribute(key string, value interface{})
}
