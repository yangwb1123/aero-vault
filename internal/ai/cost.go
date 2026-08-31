package ai

import (
	"math"
	"math/bits"
)

// Cost accounting helpers for AI usage. Cost is tracked in USD-millionths
// ("micros") as an integer to stay exact and cross-dialect friendly.

// tokensFromUsage extracts prompt/completion/total token counts from an
// OpenAI-style usage map (keys "prompt_tokens", "completion_tokens",
// "total_tokens"). Missing or negative keys yield 0; total falls back to
// prompt plus completion tokens when that sum is representable.
func tokensFromUsage(u map[string]int) (prompt, completion, total int) {
	prompt = nonNegativeToken(u["prompt_tokens"])
	completion = nonNegativeToken(u["completion_tokens"])
	total = nonNegativeToken(u["total_tokens"])
	if total == 0 {
		if prompt > math.MaxInt-completion {
			return prompt, completion, 0
		}
		total = prompt + completion
	}
	return
}

func nonNegativeToken(token int) int {
	if token < 0 {
		return 0
	}
	return token
}

// costMicros estimates cost in USD-millionths from token counts and per-1000-
// token prices (also in USD-millionths). Returns 0 when unpriced, invalid, or
// when the resulting cost cannot be represented safely.
func costMicros(promptTokens, completionTokens int, promptMicrosPer1K, completionMicrosPer1K int64) int64 {
	if promptTokens < 0 || completionTokens < 0 || promptMicrosPer1K < 0 || completionMicrosPer1K < 0 {
		return 0
	}
	promptCost, promptRemainder, ok := costPerThousand(int64(promptTokens), promptMicrosPer1K)
	if !ok {
		return 0
	}
	completionCost, completionRemainder, ok := costPerThousand(int64(completionTokens), completionMicrosPer1K)
	if !ok || promptCost > math.MaxInt64-completionCost {
		return 0
	}
	whole := promptCost + completionCost
	carry := (promptRemainder + completionRemainder) / 1000
	if whole > math.MaxInt64-carry {
		return 0
	}
	return whole + carry
}

// costPerThousand divides a non-negative 128-bit token*price product by the
// pricing scale without overflowing an int64 intermediate.
func costPerThousand(tokens, price int64) (whole, remainder int64, ok bool) {
	hi, lo := bits.Mul64(uint64(tokens), uint64(price))
	const scale uint64 = 1000
	if hi >= scale {
		return 0, 0, false
	}
	quotient, rem := bits.Div64(hi, lo, scale)
	if quotient > uint64(math.MaxInt64) {
		return 0, 0, false
	}
	return int64(quotient), int64(rem), true
}

// usdPer1KToMicros converts a USD-per-1000-tokens price to USD-millionths.
func usdPer1KToMicros(usd float64) int64 {
	return int64(usd * 1_000_000)
}
