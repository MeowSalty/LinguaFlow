package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

// EnvironmentSecret resolves an explicit value or a file, without logging
// either. present distinguishes absence from an explicitly empty direct value;
// each caller remains responsible for validating its secret's value.
func EnvironmentSecret(environment map[string]string, name, workingDirectory string) (value string, present bool, err error) {
	value, direct := environment[name]
	file, fromFile := environment[name+"_FILE"]
	if direct && fromFile {
		return "", true, fmt.Errorf("%s conflicts with %s_FILE", name, name)
	}
	if !fromFile {
		return value, direct, nil
	}
	if strings.TrimSpace(file) == "" {
		return "", true, fmt.Errorf("%s: secret file path must not be empty", name)
	}
	data, err := os.ReadFile(absolutePath(file, workingDirectory))
	if err != nil {
		return "", true, fmt.Errorf("%s: cannot read secret file", name)
	}
	value = removeFinalNewline(string(data))
	if value == "" {
		return "", true, fmt.Errorf("%s: secret file is empty", name)
	}
	return value, true, nil
}

// PrepareLocalSecret publishes a new persistent 32-byte instance secret only
// when absent. A failed read or malformed existing file never triggers replacement.
func PrepareLocalSecret(path string) (string, error) {
	secret, err := ReadLocalSecret(path)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", fmt.Errorf("generate local instance key: %w", err)
	}
	value := hex.EncodeToString(key[:])
	if _, err := credential.PublishPrivateFile(path, []byte(value+"\n")); err != nil {
		return "", fmt.Errorf("persist local instance key: %w", err)
	}
	return ReadLocalSecret(path)
}
