package cli

import (
	"encoding/json"
	"io"
)

// jsonEncode writes v as indented JSON to w. Trailing newline included
// (matches encoding/json.Encoder behaviour, useful for files).
func jsonEncode(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
