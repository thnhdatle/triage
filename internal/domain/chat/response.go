// SPDX-License-Identifier: Apache-2.0

package chat

type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
)

type Usage struct {
	PromptTokens       int
	CompletionTokens   int
	CachedPromptTokens int
}

func (u Usage) TotalTokens() int { return u.PromptTokens + u.CompletionTokens }

func (u Usage) Add(o Usage) Usage {
	return Usage{
		PromptTokens:       u.PromptTokens + o.PromptTokens,
		CompletionTokens:   u.CompletionTokens + o.CompletionTokens,
		CachedPromptTokens: u.CachedPromptTokens + o.CachedPromptTokens,
	}
}

type Choice struct {
	Index        int
	Message      Message
	FinishReason FinishReason
}

// Response is a complete chat response.
type Response struct {
	ID                string
	Model             string
	Created           int64
	Choices           []Choice
	Usage             Usage
	SystemFingerprint string
}

type ToolCallDelta struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

type Delta struct {
	Role      Role
	Content   string
	ToolCalls []ToolCallDelta
}

type ChunkChoice struct {
	Index        int
	Delta        Delta
	FinishReason FinishReason
}

// Chunk is one streamed piece of a response.
type Chunk struct {
	ID      string
	Model   string
	Created int64
	Choices []ChunkChoice
	Usage   *Usage
}
