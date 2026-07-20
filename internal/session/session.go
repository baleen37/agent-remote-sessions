package session

import (
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Provider string

const (
	Claude Provider = "claude"
	Codex  Provider = "codex"

	MaxHostBytes     = 255
	MaxNativeIDBytes = 36
	MaxCWDBytes      = 4096
	MaxTitleBytes    = 1024
)

type Candidate struct {
	Provider  Provider
	NativeID  string
	UpdatedAt time.Time
	CWD       string
	Title     string
}

type RuntimeState string

const (
	RuntimeSaved    RuntimeState = "saved"
	RuntimeRunning  RuntimeState = "running"
	RuntimeAttached RuntimeState = "attached"
)

type Runtime struct {
	State           RuntimeState
	AttachedClients int
	StartedAt       time.Time
}

type Discovered struct {
	Candidate Candidate
	Runtime   Runtime
}

type Session struct {
	Host string
	Candidate
	Runtime Runtime
}

func ValidateCandidate(candidate Candidate) error {
	if candidate.Provider != Claude && candidate.Provider != Codex {
		return fmt.Errorf("invalid provider")
	}
	if !validUUID(candidate.NativeID) {
		return fmt.Errorf("invalid native ID")
	}
	if candidate.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid updated timestamp")
	}
	if err := validateText("CWD", candidate.CWD, MaxCWDBytes, false); err != nil {
		return err
	}
	if !strings.HasPrefix(candidate.CWD, "/") {
		return fmt.Errorf("CWD must be an absolute Unix path")
	}
	if err := validateText("title", candidate.Title, MaxTitleBytes, true); err != nil {
		return err
	}
	return nil
}

func ValidateRuntime(value Runtime) error {
	switch value.State {
	case RuntimeSaved:
		if value.AttachedClients != 0 || !value.StartedAt.IsZero() {
			return fmt.Errorf("saved runtime has live metadata")
		}
	case RuntimeRunning:
		if value.AttachedClients != 0 || value.StartedAt.IsZero() {
			return fmt.Errorf("invalid running runtime")
		}
	case RuntimeAttached:
		if value.AttachedClients < 1 || value.StartedAt.IsZero() {
			return fmt.Errorf("invalid attached runtime")
		}
	default:
		return fmt.Errorf("invalid runtime state")
	}
	return nil
}

func BindDiscovered(host string, value Discovered) (Session, error) {
	if err := validateHost(host); err != nil {
		return Session{}, err
	}
	if err := ValidateCandidate(value.Candidate); err != nil {
		return Session{}, err
	}
	if err := ValidateRuntime(value.Runtime); err != nil {
		return Session{}, err
	}
	return Session{Host: host, Candidate: value.Candidate, Runtime: value.Runtime}, nil
}

func Bind(host string, candidate Candidate) (Session, error) {
	return BindDiscovered(host, Discovered{
		Candidate: candidate,
		Runtime:   Runtime{State: RuntimeSaved},
	})
}

func Project(cwd string) string {
	return path.Base(path.Clean(cwd))
}

func validateHost(host string) error {
	if err := validateText("host", host, MaxHostBytes, false); err != nil {
		return err
	}
	if host[0] == '-' {
		return fmt.Errorf("host must not begin with a dash")
	}
	for _, r := range host {
		if unicode.IsSpace(r) {
			return fmt.Errorf("host must not contain whitespace")
		}
	}
	return nil
}

func validateText(name, value string, maxBytes int, allowEmpty bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", name)
		}
	}
	return nil
}

func validUUID(value string) bool {
	if len(value) != MaxNativeIDBytes {
		return false
	}
	for i := range value {
		switch i {
		case 8, 13, 18, 23:
			if value[i] != '-' {
				return false
			}
		default:
			if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
				return false
			}
		}
	}
	return true
}
