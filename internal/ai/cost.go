package ai

// Cost accounting helpers for AI usage. Cost is tracked in USD-millionths
// ("micros") as an integer to stay exact and cross-dialect friendly.

// tokensFromUsage extracts prompt/completion/total token counts from an
// OpenAI-style usage map (keys "prompt_tokens", "completion_tokens",
// "total_tokens"). Missing keys yield 0; total falls back to prompt+completion.
func tokensFromUsage(u map[string]int) (prompt, completion, total int) {
	prompt = u["prompt_tokens"]
	completion = u["completion_tokens"]
	total = u["total_tokens"]
	if total == 0 {
		total = prompt + completion
	}
	return
}

// costMicros estimates cost in USD-millionths from token counts and per-1000-
// token prices (also in USD-millionths). Returns 0 when unpriced.
func costMicros(promptTokens, completionTokens int, promptMicrosPer1K, completionMicrosPer1K int64) int64 {
	return (int64(promptTokens)*promptMicrosPer1K + int64(completionTokens)*completionMicrosPer1K) / 1000
}

// usdPer1KToMicros converts a USD-per-1000-tokens price to USD-millionths.
func usdPer1KToMicros(usd float64) int64 {
	return int64(usd * 1_000_000)
}
