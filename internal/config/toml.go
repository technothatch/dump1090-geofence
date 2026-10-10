// Package config loads fence.toml and merges it with command line flags.
//
// TOML support is a deliberately small hand-rolled parser covering exactly
// the schema fence.toml uses: single-level [sections], scalar keys
// (strings, integers, floats, booleans) and flat arrays of scalars, with
// '#' comments. The alternative was github.com/pelletier/go-toml/v2; a
// hand-rolled parser was chosen because the schema is tiny and fixed, the
// strict parser can reject unknown keys (a typo'd deny_hex key must never
// silently mean "deny nobody"), and keeping the module dependency-free
// makes `go install` hermetic. The parser is unit tested against valid and
// invalid inputs, including multi-line arrays and comment handling.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Doc is the parsed TOML document: section name -> key -> value. Values are
// string, bool, int64, float64 or []any of those scalars. The empty section
// name holds keys that appeared before any [section] header.
type Doc map[string]map[string]any

// Parse parses a fence.toml document. Errors carry the offending line.
func Parse(data []byte) (Doc, error) {
	doc := Doc{}
	section := ""
	var (
		stmt     strings.Builder
		stmtLine int
		depth    int // bracket depth of the statement being accumulated
	)

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i, raw := range lines {
		lineNo := i + 1
		code, err := stripComment(raw)
		if err != nil {
			return nil, lineErr(lineNo, err)
		}
		trimmed := strings.TrimSpace(code)

		if trimmed == "" && stmt.Len() == 0 {
			continue
		}

		if stmt.Len() == 0 {
			stmtLine = lineNo
			if strings.HasPrefix(trimmed, "[") {
				sec, err := parseHeader(trimmed)
				if err != nil {
					return nil, lineErr(lineNo, err)
				}
				if _, ok := doc[sec]; ok {
					return nil, lineErr(lineNo, fmt.Errorf("duplicate section [%s]", sec))
				}
				doc[sec] = map[string]any{}
				section = sec
				continue
			}
			depth = 0
		} else if strings.HasPrefix(trimmed, "[") && depth == 0 {
			return nil, lineErr(lineNo, fmt.Errorf("unexpected table header inside a value"))
		}

		if err := scanBrackets(trimmed, &depth); err != nil {
			return nil, lineErr(lineNo, err)
		}
		if stmt.Len() > 0 {
			stmt.WriteByte(' ')
		}
		stmt.WriteString(trimmed)

		if depth > 0 {
			continue // multi-line array, keep accumulating
		}

		key, value, err := parseStatement(stmt.String())
		if err != nil {
			return nil, lineErr(stmtLine, err)
		}
		sec, ok := doc[section]
		if !ok {
			return nil, lineErr(stmtLine, fmt.Errorf("key %q appears before any [section] header", key))
		}
		if _, dup := sec[key]; dup {
			return nil, lineErr(stmtLine, fmt.Errorf("duplicate key %q in [%s]", key, section))
		}
		sec[key] = value
		stmt.Reset()
	}

	if stmt.Len() > 0 {
		return nil, lineErr(stmtLine, fmt.Errorf("unterminated value (missing ]?)"))
	}
	return doc, nil
}

// section returns the keys of a section, or nil when absent.
func (d Doc) section(name string) map[string]any { return d[name] }

// stripComment removes a trailing '#' comment, honouring quoted strings.
func stripComment(line string) (string, error) {
	var inStr, inLit, esc bool
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case inLit:
			if c == '\'' {
				inLit = false
			}
		case c == '"':
			inStr = true
		case c == '\'':
			inLit = true
		case c == '#':
			return line[:i], nil
		}
	}
	if inStr {
		return "", fmt.Errorf("unterminated string")
	}
	return line, nil
}

// parseHeader parses "[name]" table headers. Nested "[a.b]" headers are
// rejected: the fence.toml schema has no nested tables.
func parseHeader(s string) (string, error) {
	if !strings.HasSuffix(s, "]") {
		return "", fmt.Errorf("malformed table header %q", s)
	}
	name := strings.TrimSpace(s[1 : len(s)-1])
	if name == "" {
		return "", fmt.Errorf("empty table header")
	}
	if strings.ContainsAny(name, "[].") {
		return "", fmt.Errorf("nested tables are not supported: %q", s)
	}
	for _, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '_' || r == '-' || r == ' '
		if !valid {
			return "", fmt.Errorf("invalid table name %q", name)
		}
	}
	return name, nil
}

// scanBrackets advances depth over '[' and ']' outside of strings and
// rejects nested arrays.
func scanBrackets(s string, depth *int) error {
	var inStr, inLit, esc bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case inLit:
			if c == '\'' {
				inLit = false
			}
		case c == '"':
			inStr = true
		case c == '\'':
			inLit = true
		case c == '[':
			*depth++
			if *depth > 1 {
				return fmt.Errorf("nested arrays are not supported")
			}
		case c == ']':
			*depth--
			if *depth < 0 {
				return fmt.Errorf("unbalanced ]")
			}
		}
	}
	if inStr {
		return fmt.Errorf("unterminated string")
	}
	return nil
}

// parseStatement splits "key = value" and parses the value.
func parseStatement(s string) (key string, value any, err error) {
	eq, err := findAssign(s)
	if err != nil {
		return "", nil, err
	}
	key = strings.TrimSpace(s[:eq])
	if key == "" {
		return "", nil, fmt.Errorf("missing key before '='")
	}
	for _, r := range key {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '_' || r == '-'
		if !valid {
			return "", nil, fmt.Errorf("invalid key %q", key)
		}
	}
	value, err = parseValue(strings.TrimSpace(s[eq+1:]))
	if err != nil {
		return "", nil, fmt.Errorf("key %q: %w", key, err)
	}
	return key, value, nil
}

// findAssign locates the first '=' outside of strings at bracket depth 0.
func findAssign(s string) (int, error) {
	var inStr, inLit, esc bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case inLit:
			if c == '\'' {
				inLit = false
			}
		case c == '"':
			inStr = true
		case c == '\'':
			inLit = true
		case c == '=':
			return i, nil
		case c == '[':
			return -1, fmt.Errorf("expected '=' before array")
		}
	}
	return -1, fmt.Errorf("expected key = value")
}

// parseValue parses a scalar or a flat array of scalars.
func parseValue(s string) (any, error) {
	if s == "" {
		return nil, fmt.Errorf("missing value")
	}
	if strings.HasPrefix(s, "[") {
		return parseArray(s)
	}
	return parseScalar(s)
}

func parseArray(s string) ([]any, error) {
	if !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("unterminated array")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []any{}, nil
	}
	var (
		out   []any
		inStr bool
		esc   bool
		start int
		inLit bool
	)
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case inLit:
			if c == '\'' {
				inLit = false
			}
		case c == '"':
			inStr = true
		case c == '\'':
			inLit = true
		case c == '[':
			return nil, fmt.Errorf("nested arrays are not supported")
		case c == ',':
			elem := strings.TrimSpace(inner[start:i])
			if elem != "" {
				v, err := parseScalar(elem)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			start = i + 1
		}
	}
	tail := strings.TrimSpace(inner[start:])
	if tail != "" {
		v, err := parseScalar(tail)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// parseScalar parses a string, bool or number.
func parseScalar(s string) (any, error) {
	switch {
	case strings.HasPrefix(s, `"`):
		v, err := strconv.Unquote(s) // Go string syntax covers TOML basic strings
		if err != nil {
			return nil, fmt.Errorf("invalid string %s", s)
		}
		return v, nil
	case strings.HasPrefix(s, `'`):
		if len(s) < 2 || !strings.HasSuffix(s, "'") {
			return nil, fmt.Errorf("invalid literal string %s", s)
		}
		return s[1 : len(s)-1], nil
	case s == "true":
		return true, nil
	case s == "false":
		return false, nil
	}
	num := strings.ReplaceAll(s, "_", "")
	if i, err := strconv.ParseInt(num, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(num, 64); err == nil {
		return f, nil
	}
	return nil, fmt.Errorf("invalid value %q (strings need quotes)", s)
}

func lineErr(line int, err error) error {
	return fmt.Errorf("toml line %d: %w", line, err)
}
