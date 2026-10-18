package postgres

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// engineSpecJSON is stored in the existing command_argv JSONB column. The
// column name is retained for database compatibility, while new rows use an
// object so command and args remain separate caller-owned fields.
type engineSpecJSON struct {
	Command string `json:"command"`
}

func marshalEngineSpec(command string) ([]byte, error) {
	return json.Marshal(engineSpecJSON{Command: command})
}

func unmarshalEngineSpec(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		// Older rows stored the caller's complete command as a JSON string.
		// Keep the command opaque: it is intentionally not tokenized into argv.
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		var command string
		if err := decoder.Decode(&command); err != nil {
			return "", fmt.Errorf("decode engine command: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return "", fmt.Errorf("decode engine command: multiple JSON values")
			}
			return "", fmt.Errorf("decode engine command: trailing data: %w", err)
		}
		return command, nil
	}
	if trimmed[0] != '{' {
		return "", fmt.Errorf("invalid engine command JSON: expected object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return "", fmt.Errorf("decode engine command: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("decode engine command: multiple JSON values")
		}
		return "", fmt.Errorf("decode engine command: trailing data: %w", err)
	}
	commandRaw, ok := fields["command"]
	if !ok {
		return "", fmt.Errorf("engine command is missing")
	}
	var command string
	if err := json.Unmarshal(commandRaw, &command); err == nil {
		if len(fields) != 1 {
			return "", fmt.Errorf("engine command object must contain only command")
		}
		return command, nil
	}
	var wrapper []string
	if len(fields) != 2 {
		return "", fmt.Errorf("invalid legacy engine wrapper fields")
	}
	argsRaw, ok := fields["args"]
	if !ok {
		return "", fmt.Errorf("invalid legacy engine args wrapper")
	}
	if err := json.Unmarshal(commandRaw, &wrapper); err != nil || len(wrapper) != 2 || wrapper[0] != "/bin/sh" || wrapper[1] != "-c" {
		return "", fmt.Errorf("invalid legacy engine command wrapper")
	}
	var args []string
	if err := json.Unmarshal(argsRaw, &args); err != nil || len(args) != 1 {
		return "", fmt.Errorf("invalid legacy engine args wrapper")
	}
	return args[0], nil
}
