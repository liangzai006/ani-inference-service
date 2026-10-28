package main

import (
	"errors"
	"os"
	"strings"

	"github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
)

func configuredAccessKeyCipher() (*accesskey.SecretCipher, error) {
	path := strings.TrimSpace(os.Getenv("ANI_INFERENCE_ACCESS_KEY_ENCRYPTION_KEY_FILE"))
	if path == "" {
		return nil, errors.New("ANI_INFERENCE_ACCESS_KEY_ENCRYPTION_KEY_FILE is required")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read inference access-key encryption key file")
	}
	if len(key) != 32 {
		return nil, errors.New("inference access-key encryption key file must contain exactly 32 raw bytes")
	}
	return accesskey.NewSecretCipher(key)
}
