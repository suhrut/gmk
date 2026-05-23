// Package expr is the gmk expression language: AST, parser, and evaluator.
//
// Stage 3a supports:
//   - String/int/float/bool literals
//   - Var references: ${name}
//   - Typed refs: ${env:NAME}, ${ctx:NAME}, ${var:NAME}
//   - Modifiers (bash-compatible): ${name:-default}, ${name:?required}, ${name:+alternate}
//   - Function calls: upper(x), starts_with(s, prefix)
//   - Pipelines: ${x | upper | trim}
//   - Binary ops: ==, !=
//   - Parenthesized sub-expressions
//   - Template strings mixing literal text and ${...} substitutions
//
// Stage 3b extends with:
//   - First-class structured values (list and map) matching JSON
//   - Dot access on maps: ${user.name}
//   - Bracket indexing on lists/maps: ${list[0]}, ${map["key"]}
//   - call:fn(named=args) head-position function calls
//   - Coercion to/from JSON for the env-var bridge
//
// The Value type lattice maps 1:1 to JSON types:
//
//	None    -> null
//	Bool    -> true/false
//	Int     -> integer JSON number
//	Float   -> floating-point JSON number
//	String  -> JSON string
//	List    -> JSON array
//	Map     -> JSON object
//
// This 1:1 correspondence is the foundation of the "JSON-in/JSON-out"
// boundary contract: every callable receives and returns these types,
// every plugin gets them via JSON-RPC files, and the env-var bridge
// serializes structured values as JSON.
//
// The package has no dependency on other gmk packages — it operates over
// an interface (VarResolver) that callers implement to plug in scope
// walking. This keeps the language layer reusable across stages and
// testable in isolation.
package expr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// ValueKind tags the runtime type of a Value.
type ValueKind int

const (
	NoneKind   ValueKind = iota // unset / null (maps to JSON null)
	StringKind                  // text (maps to JSON string)
	IntKind                     // int64 (maps to JSON integer)
	FloatKind                   // float64 (maps to JSON number)
	BoolKind                    // true/false (maps to JSON bool)
	ListKind                    // []Value (maps to JSON array)
	MapKind                     // map[string]Value (maps to JSON object); Stage 3b
)

// String returns the human-readable name of a ValueKind for error messages.
func (k ValueKind) String() string {
	switch k {
	case NoneKind:
		return "none"
	case StringKind:
		return "string"
	case IntKind:
		return "int"
	case FloatKind:
		return "float"
	case BoolKind:
		return "bool"
	case ListKind:
		return "list"
	case MapKind:
		return "map"
	default:
		return "unknown"
	}
}

// Value is a runtime value in the expression language. It's a sum type
// realized as a struct with a Kind discriminator — chosen over interface{}
// because:
//   - typed access is explicit (no type assertions in eval paths)
//   - zero allocations for scalar values
//   - serialization to/from JSON is straightforward (see ToJSON/FromJSON)
//
// All accessor methods (As*) perform type coercion where defined; calling
// e.g. AsInt on a StringValue tries to parse the string. Unrepresentable
// coercions return errors.
type Value struct {
	Kind ValueKind
	Str  string
	Int  int64
	Flt  float64
	Bool bool
	List []Value
	Map  map[string]Value // Stage 3b: JSON-object-shaped values
}

// NewNone returns a None value.
func NewNone() Value { return Value{Kind: NoneKind} }

// NewString returns a String value.
func NewString(s string) Value { return Value{Kind: StringKind, Str: s} }

// NewInt returns an Int value.
func NewInt(i int64) Value { return Value{Kind: IntKind, Int: i} }

// NewFloat returns a Float value.
func NewFloat(f float64) Value { return Value{Kind: FloatKind, Flt: f} }

// NewBool returns a Bool value.
func NewBool(b bool) Value { return Value{Kind: BoolKind, Bool: b} }

// NewList returns a List value backed by the given slice (not copied).
func NewList(items []Value) Value { return Value{Kind: ListKind, List: items} }

// NewMap returns a Map value backed by the given map (not copied).
func NewMap(m map[string]Value) Value { return Value{Kind: MapKind, Map: m} }

// AsString returns the value rendered as a string. All Values are
// renderable as strings; this never returns an error. Structured types
// (List, Map) serialize as JSON for predictable round-tripping.
//
//	None    -> ""
//	String  -> the string
//	Int     -> decimal form
//	Float   -> shortest round-tripping form
//	Bool    -> "true" / "false"
//	List    -> JSON array
//	Map     -> JSON object (keys in sorted order for determinism)
func (v Value) AsString() string {
	switch v.Kind {
	case NoneKind:
		return ""
	case StringKind:
		return v.Str
	case IntKind:
		return strconv.FormatInt(v.Int, 10)
	case FloatKind:
		return strconv.FormatFloat(v.Flt, 'g', -1, 64)
	case BoolKind:
		if v.Bool {
			return "true"
		}
		return "false"
	case ListKind, MapKind:
		// JSON serialization for structured types. This is the env-var
		// bridge rule from the design: scalars export naturally,
		// structured values export as JSON.
		b, err := json.Marshal(v.ToJSON())
		if err != nil {
			return ""
		}
		return string(b)
	default:
		return ""
	}
}

// AsInt coerces the value to int64. Returns an error if coercion isn't
// possible (e.g. a string that doesn't parse as int).
func (v Value) AsInt() (int64, error) {
	switch v.Kind {
	case IntKind:
		return v.Int, nil
	case FloatKind:
		return int64(v.Flt), nil
	case BoolKind:
		if v.Bool {
			return 1, nil
		}
		return 0, nil
	case StringKind:
		n, err := strconv.ParseInt(v.Str, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot convert %q to int: %w", v.Str, err)
		}
		return n, nil
	case NoneKind:
		return 0, fmt.Errorf("cannot convert none to int")
	case ListKind, MapKind:
		return 0, fmt.Errorf("cannot convert %s to int", v.Kind)
	default:
		return 0, fmt.Errorf("cannot convert %s to int", v.Kind)
	}
}

// AsBool coerces the value to bool using gmk's truthiness rules:
//
//	Bool      -> the value
//	None      -> false
//	String    -> false if "", "0", "false", "no", "off" (case-insensitive); true otherwise
//	Int/Float -> non-zero is true
//	List      -> non-empty is true
//	Map       -> non-empty is true
//
// This mirrors common shell semantics, important for Stage 4 conditions.
func (v Value) AsBool() bool {
	switch v.Kind {
	case BoolKind:
		return v.Bool
	case NoneKind:
		return false
	case StringKind:
		if v.Str == "" {
			return false
		}
		switch lowercase(v.Str) {
		case "0", "false", "no", "off":
			return false
		}
		return true
	case IntKind:
		return v.Int != 0
	case FloatKind:
		return v.Flt != 0
	case ListKind:
		return len(v.List) > 0
	case MapKind:
		return len(v.Map) > 0
	default:
		return false
	}
}

// IsEmpty reports whether the value is "empty" for modifier purposes:
//   - None is empty
//   - "" is empty
//   - Empty list is empty
//   - Empty map is empty
//   - All other values are non-empty (including 0, false)
//
// Bash's ${var:-default} triggers on "var is unset or empty". This mirrors
// that exactly: unset vars resolve to None (empty); empty strings are also
// empty; zero and false are NOT empty (they're explicit values).
func (v Value) IsEmpty() bool {
	switch v.Kind {
	case NoneKind:
		return true
	case StringKind:
		return v.Str == ""
	case ListKind:
		return len(v.List) == 0
	case MapKind:
		return len(v.Map) == 0
	default:
		return false
	}
}

// Equal reports whether two values are equal under expression-language
// semantics. Cross-kind comparisons coerce both sides to strings.
//
// Note that StringKind "1" == IntKind 1 is true (string coerces both
// sides via AsString -> "1" == "1"). This is intentional for YAML
// ergonomics — users write quoted strings in YAML but mean numbers often.
//
// Map equality is by key set + recursive Value equality on values.
// Map equality is order-insensitive (matches JSON object semantics).
func (v Value) Equal(other Value) bool {
	if v.Kind == other.Kind {
		switch v.Kind {
		case StringKind:
			return v.Str == other.Str
		case IntKind:
			return v.Int == other.Int
		case FloatKind:
			return v.Flt == other.Flt
		case BoolKind:
			return v.Bool == other.Bool
		case NoneKind:
			return true
		case ListKind:
			if len(v.List) != len(other.List) {
				return false
			}
			for i := range v.List {
				if !v.List[i].Equal(other.List[i]) {
					return false
				}
			}
			return true
		case MapKind:
			if len(v.Map) != len(other.Map) {
				return false
			}
			for k, vv := range v.Map {
				ov, ok := other.Map[k]
				if !ok || !vv.Equal(ov) {
					return false
				}
			}
			return true
		}
	}
	return v.AsString() == other.AsString()
}

// ToJSON returns this Value as a Go-native value that encoding/json
// can marshal directly. This is the canonical bridge between our typed
// Value tree and the JSON files written to disk for callable IPC.
//
// The mapping mirrors AsString's serialization rules:
//
//	None    -> nil
//	Bool    -> bool
//	Int     -> int64
//	Float   -> float64
//	String  -> string
//	List    -> []any (recursive)
//	Map     -> map[string]any (recursive)
func (v Value) ToJSON() any {
	switch v.Kind {
	case NoneKind:
		return nil
	case BoolKind:
		return v.Bool
	case IntKind:
		return v.Int
	case FloatKind:
		return v.Flt
	case StringKind:
		return v.Str
	case ListKind:
		out := make([]any, len(v.List))
		for i, item := range v.List {
			out[i] = item.ToJSON()
		}
		return out
	case MapKind:
		out := make(map[string]any, len(v.Map))
		for k, item := range v.Map {
			out[k] = item.ToJSON()
		}
		return out
	}
	return nil
}

// FromJSON converts a Go-native JSON-shaped value (the kind json.Unmarshal
// produces) into our Value type. Used at boundaries — when reading
// args.json or a plugin's result.json.
//
// JSON numbers are parsed as int64 if they represent integers exactly
// (no decimal point in the source), else as float64. The standard
// library decodes all JSON numbers as float64 by default; callers that
// care about int-vs-float should decode using json.Decoder with
// UseNumber(), then pass json.Number values which FromJSON handles
// explicitly.
func FromJSON(v any) (Value, error) {
	switch x := v.(type) {
	case nil:
		return NewNone(), nil
	case bool:
		return NewBool(x), nil
	case int:
		return NewInt(int64(x)), nil
	case int64:
		return NewInt(x), nil
	case float64:
		// json.Unmarshal's default for numbers. If it's a whole number,
		// keep it as float here — caller can coerce via AsInt if needed.
		return NewFloat(x), nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return NewInt(i), nil
		}
		if f, err := x.Float64(); err == nil {
			return NewFloat(f), nil
		}
		return NewNone(), fmt.Errorf("invalid json.Number: %s", x)
	case string:
		return NewString(x), nil
	case []any:
		items := make([]Value, len(x))
		for i, item := range x {
			vv, err := FromJSON(item)
			if err != nil {
				return NewNone(), fmt.Errorf("list[%d]: %w", i, err)
			}
			items[i] = vv
		}
		return NewList(items), nil
	case map[string]any:
		m := make(map[string]Value, len(x))
		for k, item := range x {
			vv, err := FromJSON(item)
			if err != nil {
				return NewNone(), fmt.Errorf("map[%q]: %w", k, err)
			}
			m[k] = vv
		}
		return NewMap(m), nil
	}
	return NewNone(), fmt.Errorf("unsupported JSON type: %T", v)
}

// Index returns the element at the given key/index. Used by the bracket
// access operator: ${list[0]}, ${map["key"]}, ${map[varname]}.
//
// For lists, key must coerce to int. Negative indices count from the
// end (Python/Ruby style): list[-1] is the last element.
// For maps, key is coerced to string and used as the key.
// For other types, this is an error.
func (v Value) Index(key Value) (Value, error) {
	switch v.Kind {
	case ListKind:
		i, err := key.AsInt()
		if err != nil {
			return NewNone(), fmt.Errorf("list index must be int: %w", err)
		}
		n := int64(len(v.List))
		if i < 0 {
			i += n
		}
		if i < 0 || i >= n {
			return NewNone(), fmt.Errorf("index %d out of range [0,%d)", i, n)
		}
		return v.List[i], nil
	case MapKind:
		k := key.AsString()
		val, ok := v.Map[k]
		if !ok {
			return NewNone(), fmt.Errorf("key %q not found", k)
		}
		return val, nil
	case StringKind:
		// Strings can also be indexed by byte position. Useful for
		// substring access. UTF-8 bytes, not runes, by design — matches
		// bash/POSIX expectations.
		i, err := key.AsInt()
		if err != nil {
			return NewNone(), fmt.Errorf("string index must be int: %w", err)
		}
		n := int64(len(v.Str))
		if i < 0 {
			i += n
		}
		if i < 0 || i >= n {
			return NewNone(), fmt.Errorf("string index %d out of range [0,%d)", i, n)
		}
		return NewString(string(v.Str[i])), nil
	}
	return NewNone(), fmt.Errorf("cannot index %s", v.Kind)
}

// Field returns the value at the given map field. Convenience for dot
// access: ${user.name} → user.Field("name"). Equivalent to Index with
// a string key, but yields cleaner error messages.
func (v Value) Field(name string) (Value, error) {
	if v.Kind != MapKind {
		return NewNone(), fmt.Errorf("cannot access field %q on %s", name, v.Kind)
	}
	val, ok := v.Map[name]
	if !ok {
		return NewNone(), fmt.Errorf("no such field %q", name)
	}
	return val, nil
}

// Keys returns the map's keys as a sorted list of strings. For lists,
// returns the indices as integer strings. For other types, returns
// an empty list.
func (v Value) Keys() []string {
	switch v.Kind {
	case MapKind:
		ks := make([]string, 0, len(v.Map))
		for k := range v.Map {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	case ListKind:
		ks := make([]string, len(v.List))
		for i := range v.List {
			ks[i] = strconv.Itoa(i)
		}
		return ks
	}
	return nil
}

// lowercase is a small helper that doesn't pull in the strings package
// for one operation. ASCII-only, sufficient for the keywords in AsBool.
func lowercase(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
