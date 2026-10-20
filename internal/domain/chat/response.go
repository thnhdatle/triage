// SPDX-License-Identifier: Apache-2.0

package chat

// FinishReason is why the model stopped generating.
type FinishReason string

// Finish reasons (OpenAI vocabulary).
const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
)

// Usage counts tokens of one call or of a whole request.
type Usage struct {
	PromptTokens       int
	CompletionTokens   int
	CachedPromptTokens int
}

// TotalTokens returns prompt plus completion tokens.
func (u Usage) TotalTokens() int { return u.PromptTokens + u.CompletionTokens }

// Add returns the sum of two usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		PromptTokens:       u.PromptTokens + o.PromptTokens,
		CompletionTokens:   u.CompletionTokens + o.CompletionTokens,
		CachedPromptTokens: u.CachedPromptTokens + o.CachedPromptTokens,
	}
}

// Choice is one completion alternative of a non-streaming response.
type Choice struct {
	Index        int
	Message      Message
	FinishReason FinishReason
}

// Response is a provider-neutral non-streaming chat completion.
type Response struct {
	ID      string
	Model   string
	Created int64
	Choices []Choice
	Usage   Usage
	// SystemFingerprint is passed through when the provider sets it.
	SystemFingerprint string
}

// ToolCallDelta is a fragment of a tool call in a stream. Index identifies the
// call; ID, Type and Name usually arrive in the first fragment only.
type ToolCallDelta struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

// Delta is the incremental content of a stream chunk choice.
type Delta struct {
	Role      Role
	Content   string
	ToolCalls []ToolCallDelta
}

// ChunkChoice is one choice of a stream chunk.
type ChunkChoice struct {
	Index        int
	Delta        Delta
	FinishReason FinishReason
}

// Chunk is one event of a streaming chat completion.
type Chunk struct {
	ID      string
	Model   string
	Created int64
	Choices []ChunkChoice
	// Usage is set on the final usage chunk only.
	Usage *Usage
}
