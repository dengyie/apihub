package constant

// AnthropicVersionDefault is the protocol baseline every Anthropic-protocol
// upstream request carries when the client does not pin one. Single source of
// truth for the claude / newapi / ollama / advancedcustom adaptors and the
// upstream client fingerprint filler — keep them on this constant so the
// default can never drift between paths.
const AnthropicVersionDefault = "2023-06-01"
