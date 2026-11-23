package bundle

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	maxDepth       = 24
	maxTokens      = 2_000_000
	maxMembers     = 50_000
	maxFields      = 1024
	maxStringBytes = 64 << 10
	maxResources   = 50_000
)

// The token pass bounds parser work before typed JSON decode and rejects
// duplicate keys, including case aliases accepted by encoding/json.
func validateJSONStructure(raw []byte) error {
	if !utf8.Valid(raw) {
		return ErrFormat
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	remaining := maxTokens
	var value func(int) error
	take := func() (json.Token, error) {
		if remaining == 0 {
			return nil, ErrLimit
		}
		remaining--
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrFormat
		}
		switch typed := token.(type) {
		case string:
			if len(typed) > maxStringBytes {
				return nil, ErrLimit
			}
		case json.Number:
			if len(typed) > 128 {
				return nil, ErrLimit
			}
		}
		return token, nil
	}
	value = func(depth int) error {
		if depth > maxDepth {
			return ErrLimit
		}
		token, err := take()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			seen := make(map[string]bool)
			for decoder.More() {
				if len(seen) == maxFields {
					return ErrLimit
				}
				rawKey, err := take()
				if err != nil {
					return err
				}
				key, isKey := rawKey.(string)
				if !isKey || key == "" {
					return ErrFormat
				}
				key = strings.ToLower(key)
				if seen[key] {
					return ErrFormat
				}
				seen[key] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := take()
			if err != nil || end != json.Delim('}') {
				return ErrFormat
			}
		case json.Delim('['):
			for i := 0; decoder.More(); i++ {
				if i == maxMembers {
					return ErrLimit
				}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := take()
			if err != nil || end != json.Delim(']') {
				return ErrFormat
			}
		case json.Delim('}'), json.Delim(']'):
			return ErrFormat
		}
		return nil
	}
	if err := value(1); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrFormat
	}
	return nil
}

func decodeStrict(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrFormat
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrFormat
	}
	return nil
}
