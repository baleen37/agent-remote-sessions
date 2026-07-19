package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type Host struct {
	Target string
}

func ConfigPath() (string, error) {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "ars", "hosts"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "ars", "hosts"), nil
}

func Load(path string) ([]Host, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open host inventory: %w", err)
	}
	defer file.Close()

	var hosts []Host
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		target := scanner.Text()
		trimmed := strings.TrimSpace(target)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if err := validateTarget(target); err != nil {
			return nil, fmt.Errorf("invalid host inventory line %d: %w", lineNumber, err)
		}
		if _, exists := seen[target]; exists {
			return nil, fmt.Errorf("duplicate host inventory target at line %d", lineNumber)
		}
		seen[target] = struct{}{}
		hosts = append(hosts, Host{Target: target})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read host inventory: %w", err)
	}
	return hosts, nil
}

func Add(path string, target string) error {
	if err := validateTarget(target); err != nil {
		return err
	}

	contents, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read host inventory: %w", err)
	}
	if err == nil {
		hosts, loadErr := Load(path)
		if loadErr != nil {
			return loadErr
		}
		for _, host := range hosts {
			if host.Target == target {
				return fmt.Errorf("host target is already configured")
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create host inventory directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open host inventory for append: %w", err)
	}

	line := target + "\n"
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		line = "\n" + line
	}
	if _, err := io.WriteString(file, line); err != nil {
		_ = file.Close()
		return fmt.Errorf("append host inventory: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close host inventory: %w", err)
	}
	return nil
}

func validateTarget(target string) error {
	if !utf8.ValidString(target) {
		return fmt.Errorf("host must be valid UTF-8")
	}
	if target == "" {
		return fmt.Errorf("host must not be empty")
	}
	if len(target) > session.MaxHostBytes {
		return fmt.Errorf("host exceeds %d bytes", session.MaxHostBytes)
	}
	if target[0] == '-' {
		return fmt.Errorf("host must not begin with a dash")
	}
	for _, r := range target {
		if unicode.IsControl(r) {
			return fmt.Errorf("host must not contain control characters")
		}
		if unicode.IsSpace(r) {
			return fmt.Errorf("host must not contain whitespace")
		}
	}
	return nil
}

func Select(hosts []Host, target string) ([]Host, error) {
	if target == "" {
		return hosts, nil
	}
	for _, host := range hosts {
		if host.Target == target {
			return []Host{host}, nil
		}
	}
	return nil, fmt.Errorf("host target is not configured")
}
