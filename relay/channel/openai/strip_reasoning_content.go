package openai

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// stripEmptyContentOnReasoningFrame drops an explicitly present but empty
// "content" field from the delta of a chat-completions stream frame that also
// carries non-empty reasoning.
//
// Some upstreams emit {"content":"", "reasoning_content":"word"} on every
// reasoning frame. A parser that tests the value is unaffected, but a client
// that treats the mere presence of the content field as "a text block starts
// here" closes and reopens its thinking block on every frame, so a thinking
// trace renders as one short block per word.
//
// Only that exact shape is rewritten. Frames without reasoning, frames whose
// content is non-empty or null, and the role-opening frame (which carries
// empty reasoning as well) come back unchanged. Every other field survives
// verbatim, because this runs on the passthrough path where upstream field
// sets we do not model still have to reach the client intact.
func stripEmptyContentOnReasoningFrame(data string) string {
	// Cheap gate: the shape is impossible without a reasoning field, and this
	// keeps the common content/tool frames off the decoder entirely.
	if !strings.Contains(data, `"reasoning`) {
		return data
	}
	out, changed := rewriteStream(data)
	if !changed {
		return data
	}
	return out
}

type kvPair struct {
	key string
	raw json.RawMessage
}

// orderedMembers decodes a JSON object while keeping member order, which a
// plain map would scramble. Raw values are kept as bytes so untouched fields
// are re-emitted exactly as the upstream sent them.
type orderedMembers []kvPair

func (o *orderedMembers) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errUnexpectedShape
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return errUnexpectedShape
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		*o = append(*o, kvPair{key: key, raw: raw})
	}
	_, err = dec.Token() // closing '}'
	return err
}

func (o orderedMembers) encode() []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(strconv.Quote(m.key))
		buf.WriteByte(':')
		buf.Write(m.raw)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func rewriteStream(data string) (string, bool) {
	var top orderedMembers
	if err := json.Unmarshal([]byte(data), &top); err != nil {
		return data, false
	}
	changed := false
	for i := range top {
		if top[i].key != "choices" {
			continue
		}
		var choices orderedChoices
		if err := json.Unmarshal(top[i].raw, &choices); err != nil {
			continue
		}
		if !choices.rewriteDeltaEmptyContent() {
			continue
		}
		raw, err := choices.encode()
		if err != nil {
			continue
		}
		top[i].raw = raw
		changed = true
	}
	if !changed {
		return data, false
	}
	return string(top.encode()), true
}

// orderedChoices is the choices array, kept as raw elements so choices we do
// not touch are written back byte for byte.
type orderedChoices []json.RawMessage

func (c *orderedChoices) UnmarshalJSON(b []byte) error {
	var items []json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		return err
	}
	*c = items
	return nil
}

func (c orderedChoices) encode() ([]byte, error) {
	return json.Marshal([]json.RawMessage(c))
}

func (c *orderedChoices) rewriteDeltaEmptyContent() bool {
	changed := false
	for i := range *c {
		var choice orderedMembers
		if err := json.Unmarshal((*c)[i], &choice); err != nil {
			continue
		}
		choiceChanged := false
		for j := range choice {
			if choice[j].key != "delta" {
				continue
			}
			var delta orderedMembers
			if err := json.Unmarshal(choice[j].raw, &delta); err != nil {
				continue
			}
			stripped, ok := delta.stripEmptyContentOnReasoning()
			if !ok {
				continue
			}
			choice[j].raw = stripped.encode()
			choiceChanged = true
		}
		if !choiceChanged {
			continue
		}
		(*c)[i] = choice.encode()
		changed = true
	}
	return changed
}

// stripEmptyContentOnReasoning reports whether this delta has an empty string
// "content" alongside non-empty reasoning, and returns the delta without it.
func (d orderedMembers) stripEmptyContentOnReasoning() (orderedMembers, bool) {
	hasReasoning := false
	for _, m := range d {
		if m.key != "reasoning_content" && m.key != "reasoning" {
			continue
		}
		if s, isString := jsonStringValue(m.raw); isString && s != "" {
			hasReasoning = true
			break
		}
	}
	if !hasReasoning {
		return nil, false
	}
	out := make(orderedMembers, 0, len(d))
	dropped := false
	for _, m := range d {
		if isEmptyStringMember(m.key, m.raw) {
			dropped = true
			continue
		}
		out = append(out, m)
	}
	if !dropped {
		return nil, false
	}
	return out, true
}

func isEmptyStringMember(key string, raw json.RawMessage) bool {
	if key != "content" {
		return false
	}
	s, isString := jsonStringValue(raw)
	return isString && s == ""
}

// jsonStringValue reports the value only when raw really is a JSON string.
// Unmarshalling null into a string succeeds without touching the target, so
// the leading quote has to be checked explicitly or null would read as "".
func jsonStringValue(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return "", false
	}
	return s, true
}

var errUnexpectedShape = &shapeError{}

type shapeError struct{}

func (*shapeError) Error() string { return "unexpected json shape" }
