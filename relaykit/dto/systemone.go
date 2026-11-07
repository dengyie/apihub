package dto

import (
	"encoding/json"
	"net/http"

	"github.com/dengyie/apihub/relaykit/types"
)

// SystemOneRequest is the wire format of the System One typed-decision
// protocol (jev family, also spoken by MindsHub/FuturePPO gateways): one
// state plus a named map of typed questions, answered by one calibrated
// verdict per question. State and questions stay raw because their inner
// shapes are business-owned and must pass through untouched.
type SystemOneRequest struct {
	Model     string          `json:"model"`
	State     json.RawMessage `json:"state,omitempty"`
	Questions json.RawMessage `json:"questions"`
}

func (r *SystemOneRequest) GetTokenCountMeta() *types.TokenCountMeta {
	return &types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
	}
}

func (r *SystemOneRequest) IsStream(c *http.Request) bool {
	return false
}

func (r *SystemOneRequest) SetModelName(modelName string) {
	r.Model = modelName
}
