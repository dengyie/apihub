package billing_setting

// Built-in token prices use actual USD per million tokens. Keep new model
// defaults here instead of splitting them across the legacy ratio tables.
//
// Pricing verified 2026-09-28 from official provider pricing pages:
// - OpenAI: https://developers.openai.com/api/docs/pricing
// - Claude: https://platform.claude.com/docs/en/pricing
// - DeepSeek: https://api-docs.deepseek.com/quick_start/pricing (off-peak)
// - GLM: Z.ai official pricing
// - Gemini: https://ai.google.dev/pricing
// - Grok: xAI official pricing
// - Kimi: Moonshot AI official pricing
// - Mistral: Mistral AI official pricing
//
// Free-tier models use tier("free", fixed(0)) for explicit zero pricing.
// Image/video generation models use fixed per-request pricing.
var builtinBillingExpr = map[string]string{
	// https://developers.openai.com/api/docs/pricing (Standard, 2026-09-09).
	// The Images API reports image output in output_tokens, normalized to c.
	"gpt-image-2":            `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5-sunburst": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5-flare":    `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	// https://developers.openai.com/api/docs/models/gpt-6-astra
	// Standard pricing; the long-context rates apply to the whole request.
	// Do not infer service-tier discounts from incoming request parameters:
	// channels filter service_tier by default, so it may not reach the upstream.
	"gpt-6-astra": `len <= 272000 ? tier("standard", p * 10 + c * 50 + cr * 1 + cc * 12.5) : tier("long_context", p * 20 + c * 75 + cr * 2 + cc * 25)`,

	// OpenAI GPT (text)
	"gpt-5.2": `tier("standard", p * 1.75 + c * 14 + cr * 0.175)`,
	"gpt-5.3-codex-spark": `tier("standard", p * 1.75 + c * 14 + cr * 0.175)`,
	"gpt-5.4": `tier("standard", p * 2.5 + c * 15 + cr * 0.25)`,
	"gpt-5.4-mini": `tier("standard", p * 0.75 + c * 4.5 + cr * 0.075)`,
	"gpt-5.4-nano": `tier("standard", p * 0.2 + c * 1.25 + cr * 0.02)`,
	"gpt-5.4-openai-compact": `tier("standard", p * 2.5 + c * 15 + cr * 0.25)`,
	"gpt-5.5": `tier("standard", p * 5 + c * 30 + cr * 0.5)`,
	"gpt-5.5-openai-compact": `tier("standard", p * 5 + c * 30 + cr * 0.5)`,
	"gpt-5.6": `tier("standard", p * 4 + c * 20 + cr * 0.4)`,
	"gpt-5.6-luna": `tier("standard", p * 0.2 + c * 1.2 + cr * 0.02)`,
	"gpt-5.6-sol": `tier("standard", p * 4 + c * 20 + cr * 0.4)`,
	"gpt-5.6-terra": `tier("standard", p * 2 + c * 12 + cr * 0.2)`,
	"gpt-6-luna": `tier("standard", p * 0.1 + c * 0.5 + cr * 0.01)`,
	"gpt-6-sol": `tier("standard", p * 2 + c * 10 + cr * 0.2)`,
	"openai/gpt-6-luna": `tier("standard", p * 0.1 + c * 0.5 + cr * 0.01)`,

	// OpenAI Image
	"fal-ai/gpt-image-2": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-1": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-1.5": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2-4k": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"gpt-image-2.5": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"image-1": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,
	"image-2": `tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)`,

	// Claude
	"claude-fable-5": `tier("standard", p * 10 + c * 50 + cr * 1.0 + cc * 12.5)`,
	"claude-fable-5-1": `tier("standard", p * 10 + c * 50 + cr * 0.25 + cc * 12.5)`,
	"claude-haiku-4-5": `tier("standard", p * 1 + c * 5 + cr * 0.1 + cc * 1.25)`,
	"claude-haiku-4-5-20251001": `tier("standard", p * 1 + c * 5 + cr * 0.1 + cc * 1.25)`,
	"claude-opus-4-1-20250805": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4-20250514": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4-5-20251101": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4-6": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4-6-thinking": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4-7": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4-8": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4.6-thinking": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4.8": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-4.8-thinking": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-5": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-opus-5-5": `tier("standard", p * 4 + c * 20 + cr * 0.2 + cc * 5.0)`,
	"claude-opus-5-thinking": `tier("standard", p * 5 + c * 25 + cr * 0.5 + cc * 6.25)`,
	"claude-sonnet-4-20250514": `tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75)`,
	"claude-sonnet-4-5-20250929": `tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75)`,
	"claude-sonnet-4-6": `tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75)`,
	"claude-sonnet-4-6-thinking": `tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75)`,
	"claude-sonnet-5": `tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75)`,

	// DeepSeek
	"DeepSeek-V4-Flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"DeepSeek-V4-Flash-0731": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"DeepSeek-V4-Flash-0731-think": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"DeepSeek-V4-Flash-Vision-Exp": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"DeepSeek-V4-Pro": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"DeepSeek-V4-Pro-0813": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"DeepSeek-V4-Pro-0813-think": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"Deepseek-v4-flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-ai/DeepSeek-V4-Flash-0731": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-ai/DeepSeek-V4-Pro-0813": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"deepseek-ai/DeepSeek-V4.1-Flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-ai/deepseek-v4-flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-ai/deepseek-v4-flash-0731": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-ai/deepseek-v4-pro": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"deepseek-ai/deepseek-v4-pro-0813": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"deepseek-v4-flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-v4-flash-0731": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-v4-flash-260425": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-v4-flash-vision-exp": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-v4-flash:0731": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek-v4-pro": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"deepseek-v4-pro-0813": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"deepseek-v4-pro-ga-260813": `tier("standard", p * 0.66 + c * 1.98 + cr * 0.022)`,
	"deepseek-v4.1-flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek/deepseek-v4-flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"deepseek/deepseek-v4.1-flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,
	"evomap-deepseek-v4-flash": `tier("standard", p * 0.15 + c * 0.6 + cr * 0.003)`,

	// GLM (Zhipu)
	"GLM-5.2": `tier("standard", p * 1.0 + c * 3.2)`,
	"GLM-5.3": `tier("standard", p * 1.0 + c * 3.2)`,
	"GLM-5.3-1M": `tier("standard", p * 1.0 + c * 3.2)`,
	"GLM-5.3-Flash": `tier("standard", p * 0.15 + c * 0.5)`,
	"ZhipuAI/GLM-5.2": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5-2": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5-2-260617": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5.1": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5.2": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5.2-search": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5.2-thinking": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5.3": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5.3-200k": `tier("standard", p * 1.0 + c * 3.2)`,
	"glm-5.3-flash": `tier("standard", p * 0.15 + c * 0.5)`,
	"glm-5.3-flash-free": `tier("free", fixed(0))`,
	"glm5": `tier("standard", p * 1.0 + c * 3.2)`,
	"nvidia/glm-5.3-flash": `tier("standard", p * 0.15 + c * 0.5)`,
	"z-ai/glm-5.2": `tier("standard", p * 1.0 + c * 3.2)`,
	"z-ai/glm-5.3": `tier("standard", p * 1.0 + c * 3.2)`,
	"z-ai/glm-5.3-flash": `tier("standard", p * 0.15 + c * 0.5)`,
	"z-ai/glm-5.3-free": `tier("free", fixed(0))`,
	"zai-org/GLM-5.3-Flash": `tier("standard", p * 0.15 + c * 0.5)`,
	"智谱/GLM-5.2": `tier("standard", p * 1.0 + c * 3.2)`,

	// Gemini
	"gemini-2.5-pro": `tier("standard", p * 1.25 + c * 10)`,
	"gemini-3.1-flash-lite": `tier("standard", p * 0.25 + c * 1.5)`,
	"gemini-3.1-pro": `tier("standard", p * 2.0 + c * 12)`,
	"gemini-3.1-pro-preview": `tier("standard", p * 2.0 + c * 12)`,
	"gemini-3.5-flash": `tier("standard", p * 1.5 + c * 9.0)`,
	"gemini-3.6-flash": `tier("standard", p * 0.75 + c * 3.75)`,
	"gemini-3.7-flash": `tier("standard", p * 0.75 + c * 3.75)`,
	"gemini-3.7-flash-high": `tier("standard", p * 0.75 + c * 3.75)`,
	"gemini-3.8-flash": `tier("standard", p * 0.75 + c * 3.75)`,
	"gemini-3.8-flash-high": `tier("standard", p * 0.75 + c * 3.75)`,

	// Grok (text)
	"grok-4.20-0309-non-reasoning": `tier("standard", p * 2.0 + c * 6.0)`,
	"grok-4.20-0309-reasoning": `tier("standard", p * 2.0 + c * 6.0)`,
	"grok-4.20-multi-agent-0309": `tier("standard", p * 2.0 + c * 6.0)`,
	"grok-4.3": `tier("standard", p * 1.25 + c * 2.5)`,
	"grok-4.5": `tier("standard", p * 2.0 + c * 6.0)`,
	"grok-4.5#1": `tier("standard", p * 2.0 + c * 6.0)`,
	"grok-4.5#2": `tier("standard", p * 2.0 + c * 6.0)`,
	"grok-4.6": `tier("standard", p * 2.0 + c * 6.0)`,
	"grok-4.7": `tier("standard", p * 2.0 + c * 6.0)`,
	"grok-build-0.1": `tier("standard", p * 2.0 + c * 6.0)`,

	// Grok Imagine (image/video)
	"grok-imagine-image": `tier("standard", fixed(0.05))`,
	"grok-imagine-image-2.0": `tier("standard", fixed(0.05))`,
	"grok-imagine-image-lite": `tier("standard", fixed(0.05))`,
	"grok-imagine-image-quality": `tier("standard", fixed(0.05))`,
	"grok-imagine-video": `tier("standard", fixed(0.05))`,
	"grok-imagine-video-1.5": `tier("standard", fixed(0.05))`,
	"grok-imagine-video-1.5-preview": `tier("standard", fixed(0.05))`,

	// Kimi (Moonshot)
	"Kimi-K3": `tier("standard", p * 3.0 + c * 15.0 + cr * 0.3)`,
	"k3": `tier("standard", p * 3.0 + c * 15.0 + cr * 0.3)`,
	"kimi-k2.6": `tier("standard", p * 0.95 + c * 4.0)`,
	"kimi-k2.7": `tier("standard", p * 1.5 + c * 7.5)`,
	"kimi-k3": `tier("standard", p * 3.0 + c * 15.0 + cr * 0.3)`,
	"moonshotai/kimi-k2.6": `tier("standard", p * 0.95 + c * 4.0)`,
	"moonshotai/kimi-k3": `tier("standard", p * 3.0 + c * 15.0 + cr * 0.3)`,

	// MiniMax
	"MiniMax-M3": `tier("standard", p * 0.5 + c * 2.0)`,
	"minimax-m3": `tier("standard", p * 0.5 + c * 2.0)`,

	// Mistral
	"mistral-large-latest": `tier("standard", p * 0.5 + c * 1.5)`,
	"mistral-medium-latest": `tier("standard", p * 1.5 + c * 7.5)`,
	"mistral-ocr-latest": `tier("standard", p * 0.5 + c * 1.5)`,
	"mistral-small-latest": `tier("standard", p * 0.15 + c * 0.6)`,

	// MiMo (Xiaomi)
	"mimo-v2.5": `tier("standard", p * 1.0 + c * 3.0)`,
	"mimo-v2.5-free": `tier("free", fixed(0))`,
	"mimo-v2.5-pro": `tier("standard", p * 1.0 + c * 3.0)`,
	"mimo-v2.5-tts": `tier("standard", fixed(0.01))`,
	"mimo-v2.5-tts-voiceclone": `tier("standard", fixed(0.01))`,
	"mimo-v2.5-tts-voicedesign": `tier("standard", fixed(0.01))`,
	"mimo-v2.5:free": `tier("free", fixed(0))`,
	"mimo-v2.6-flash:free": `tier("free", fixed(0))`,
	"xiaomi/mimo-v2.5-pro": `tier("standard", p * 1.0 + c * 3.0)`,

	// TTS
	"Spark-TTS-0.5B": `tier("standard", fixed(0.01))`,

	// Qwen
	"qwen3.8-flash": `tier("standard", p * 0.6 + c * 3.6)`,

	// Nano Banana (image)
	"Nano Banana 2 Lite": `tier("standard", fixed(0.05))`,
	"Nano Banana Pro": `tier("standard", fixed(0.05))`,
	"fal-ai/nano-banana-2": `tier("standard", fixed(0.05))`,
	"fal-ai/nano-banana-pro": `tier("standard", fixed(0.05))`,
	"nano-banana-2": `tier("standard", fixed(0.05))`,
	"nano-banana-2-lite": `tier("standard", fixed(0.05))`,
	"nano-banana-pro": `tier("standard", fixed(0.05))`,

	// Other
	"Qwen3Guard-Gen-0.6B": `tier("standard", p * 0.1 + c * 0.2)`,
	"Security-semantic-filtering": `tier("standard", p * 0.1 + c * 0.2)`,
	"codex-auto-review": `tier("standard", p * 1.75 + c * 14 + cr * 0.175)`,
	"hy3": `tier("standard", p * 0.5 + c * 1.5)`,
	"muse-spark-1.3": `tier("standard", p * 1.0 + c * 3.0)`,
	"tencent/Hy-MT2-30B-A3B": `tier("standard", p * 0.5 + c * 1.5)`,

	// Free tier (explicit zero)
	"cline-free/deepseek-v4.1-flash": `tier("free", fixed(0))`,
	"deepseek-v4-flash-0731-free-3": `tier("free", fixed(0))`,
	"deepseek-v4-flash-free": `tier("free", fixed(0))`,
	"deepseek-v4-flash:free": `tier("free", fixed(0))`,
	"deepseek-v4-pro-free": `tier("free", fixed(0))`,
	"deepseek-v4.1-flash:free": `tier("free", fixed(0))`,
	"hy3-free": `tier("free", fixed(0))`,
	"nemotron-3-ultra-free": `tier("free", fixed(0))`,
	"north-mini-code-free": `tier("free", fixed(0))`,
	"openrouter/free": `tier("free", fixed(0))`,
	"qwen3.8-flash-free": `tier("free", fixed(0))`,
	"qwen3.8-flash:free": `tier("free", fixed(0))`,
	"space-bunny": `tier("free", fixed(0))`,
	"space-bunny-free": `tier("free", fixed(0))`,
	"stealth/space-bunny-alpha": `tier("free", fixed(0))`,
	"x-preview-f-free": `tier("free", fixed(0))`,
	"zerank-1": `tier("free", fixed(0))`,
	"zerank-1-small": `tier("free", fixed(0))`,
	"zerank-2": `tier("free", fixed(0))`,

}
