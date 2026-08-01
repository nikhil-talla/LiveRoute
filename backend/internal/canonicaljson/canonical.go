package canonicaljson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	rfc8785 "github.com/gibson042/canonicaljson-go"
)

// Marshal validates one JSON value, rejects duplicate object members, and
// returns RFC 8785 canonical bytes suitable for durable command identity.
func Marshal(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decode(decoder)
	if err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON value")
		}
		return nil, fmt.Errorf("trailing JSON data: %w", err)
	}
	return rfc8785.Marshal(value)
}

func decode(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			object := make(map[string]any)
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object member name is not a string")
				}
				if _, exists := seen[key]; exists {
					return nil, fmt.Errorf("duplicate object member %q", key)
				}
				seen[key] = struct{}{}
				member, err := decode(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = member
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("invalid object terminator")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				member, err := decode(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, member)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("invalid array terminator")
			}
			return array, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	default:
		return token, nil
	}
}
