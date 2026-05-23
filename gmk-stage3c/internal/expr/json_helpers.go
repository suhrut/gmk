package expr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// jsonMarshalCompact serializes a Go value (typically the output of
// Value.ToJSON) to a single-line JSON string. Used by the to_json
// builtin to produce strings that round-trip cleanly through env vars
// and shell quoting.
func jsonMarshalCompact(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // we don't want <>&" escaped — they're valid JSON
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	// json.Encoder adds a trailing newline; strip it.
	return strings.TrimRight(buf.String(), "\n"), nil
}

// jsonUnmarshalToValue parses a JSON string into a Value. Uses
// json.Decoder with UseNumber so integer literals come through as
// json.Number rather than float64, preserving int-vs-float distinction.
//
// Returns an error if the input is empty or not valid JSON.
func jsonUnmarshalToValue(s string) (Value, error) {
	if s == "" {
		return NewNone(), fmt.Errorf("empty input")
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return NewNone(), err
	}
	return FromJSON(raw)
}
