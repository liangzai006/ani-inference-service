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
	if trimmed[0] != '{' {
		return "", fmt.Errorf("invalid engine command JSON: expected object")
	}
	var current struct {
		Command json.RawMessage `json:"command"`
		Args    json.RawMessage `json:"args"`
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&current); err != nil {
		return "", fmt.Errorf("decode engine command: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("decode engine command: multiple JSON values")
		}
		return "", fmt.Errorf("decode engine command: trailing data: %w", err)
	}
	if len(current.Command) == 0 {
		return "", fmt.Errorf("engine command is missing")
	}
	var command string
	if err := json.Unmarshal(current.Command, &command); err == nil {
		if len(current.Args) != 0 && !bytes.Equal(bytes.TrimSpace(current.Args), []byte("null")) {
			return "", fmt.Errorf("engine command object must not contain args")
		}
		return command, nil
	}
	var wrapper []string
	if err := json.Unmarshal(current.Command, &wrapper); err != nil || len(wrapper) != 2 || wrapper[0] != "/bin/sh" || wrapper[1] != "-c" {
		return "", fmt.Errorf("invalid legacy engine command wrapper")
	}
	var args []string
	if err := json.Unmarshal(current.Args, &args); err != nil || len(args) != 1 {
		return "", fmt.Errorf("invalid legacy engine args wrapper")
	}
	return args[0], nil
}
