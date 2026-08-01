package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	clientSchemaURL = "https://liveroute.dev/schema/liveroute.v1/client-envelope"
	serverSchemaURL = "https://liveroute.dev/schema/liveroute.v1/server-envelope"
)

// EnvelopeValidator validates the strict JSON envelope before a message is
// admitted to the connection queues. The schema files are supplied by the
// application so this package does not own or duplicate repository assets.
type EnvelopeValidator struct {
	client *jsonschema.Schema
	server *jsonschema.Schema
}

func NewEnvelopeValidator(clientDocument, serverDocument []byte) (*EnvelopeValidator, error) {
	client, err := compileEnvelopeSchema(clientSchemaURL, clientDocument)
	if err != nil {
		return nil, fmt.Errorf("compile client websocket schema: %w", err)
	}
	server, err := compileEnvelopeSchema(serverSchemaURL, serverDocument)
	if err != nil {
		return nil, fmt.Errorf("compile server websocket schema: %w", err)
	}
	return &EnvelopeValidator{client: client, server: server}, nil
}

func compileEnvelopeSchema(uri string, document []byte) (*jsonschema.Schema, error) {
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(uri, value); err != nil {
		return nil, err
	}
	return compiler.Compile(uri)
}

func (v *EnvelopeValidator) ValidateClient(raw []byte) (map[string]any, error) {
	value, err := decodeStrictJSON(raw)
	if err != nil {
		return nil, err
	}
	if err := v.client.Validate(value); err != nil {
		return nil, fmt.Errorf("client websocket envelope: %w", err)
	}
	envelope, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("client websocket envelope must be an object")
	}
	return envelope, nil
}

func (v *EnvelopeValidator) ValidateServer(raw []byte) error {
	value, err := decodeStrictJSON(raw)
	if err != nil {
		return err
	}
	if err := v.server.Validate(value); err != nil {
		return fmt.Errorf("server websocket envelope: %w", err)
	}
	return nil
}

// decodeStrictJSON rejects duplicate object members. encoding/json otherwise
// silently keeps the last member, which would let the schema validator and
// the handler observe different effective messages.
func decodeStrictJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
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
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
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
				member, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = member
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("invalid object terminator")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				member, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, member)
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("invalid array terminator")
			}
			return array, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", token)
		}
	default:
		return token, nil
	}
}
