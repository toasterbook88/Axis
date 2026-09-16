package agent

// usageResponse creates streaming chunks whose final chunk carries real
// token counts, like a modern Ollama daemon reports.
func usageResponse(promptTokens, evalTokens int, text string) []mockStreamChunk {
	return []mockStreamChunk{
		{Message: mockChunkMessage{Role: "assistant", Content: text}, Done: false},
		{Done: true, PromptEvalCount: promptTokens, EvalCount: evalTokens},
	}
}
