package bivia

import "encoding/json"

// biviaRequest is the native Articul8 Bivia decision request. Candidates map
// per endpoint: /decide takes outcomes, /choice takes choices, /set takes
// options, /score takes range+buckets, /extract takes fields.
type biviaRequest struct {
	State        string    `json:"state,omitempty"`
	Question     string    `json:"question,omitempty"`
	Outcomes     []string  `json:"outcomes,omitempty"`
	Choices      []string  `json:"choices,omitempty"`
	Options      []string  `json:"options,omitempty"`
	Range        []float64 `json:"range,omitempty"`
	Buckets      *int      `json:"buckets,omitempty"`
	Fields       []string  `json:"fields,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	AllowAbstain *bool     `json:"allow_abstain,omitempty"`
}

// biviaInstruction is the client-facing structured decision instruction: a
// JSON object with a "question" key embedded in message content. It is a
// defined type sharing biviaRequest's shape on purpose — the two roles speak
// the same wire fields, so a new field can never be added to one and silently
// dropped from the other.
type biviaInstruction biviaRequest

// biviaResponse is the native decision envelope. Value stays raw because its
// JSON type depends on the decision kind (string, number, array, object).
// /extract diverges from the other endpoints: it reports per-field verdicts in
// values (with found flags) and omits the status/probability fields entirely.
type biviaResponse struct {
	Type           string             `json:"type"`
	Value          json.RawMessage    `json:"value"`
	Values         json.RawMessage    `json:"values"`
	Probability    float64            `json:"probability"`
	Distribution   map[string]float64 `json:"distribution"`
	Status         *string            `json:"status"`
	AbstainReason  *string            `json:"abstain_reason"`
	Mode           string             `json:"mode"`
	Evidence       json.RawMessage    `json:"evidence"`
	LatencyMs      float64            `json:"latency_ms"`
	Model          string             `json:"model"`
	Policy         string             `json:"policy"`
	InputTokens    int                `json:"input_tokens"`
	ImageTokens    int                `json:"image_tokens"`
	BillableTokens int                `json:"billable_tokens"`
}

// biviaDecision is the client-facing JSON envelope returned as the assistant
// message content: one typed, calibrated decision instead of free-form text.
type biviaDecision struct {
	Type          string             `json:"type"`
	Value         json.RawMessage    `json:"value"`
	Probability   float64            `json:"probability,omitempty"`
	Distribution  map[string]float64 `json:"distribution,omitempty"`
	Status        string             `json:"status"`
	AbstainReason string             `json:"abstain_reason,omitempty"`
	Mode          string             `json:"mode,omitempty"`
	Model         string             `json:"model,omitempty"`
	Policy        string             `json:"policy,omitempty"`
	LatencyMs     float64            `json:"latency_ms,omitempty"`
	Evidence      json.RawMessage    `json:"evidence,omitempty"`
}
