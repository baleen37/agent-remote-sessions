package output

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type HostStatus string

const (
	HostOK          HostStatus = "ok"
	HostStatusError HostStatus = "error"
)

type HostResult struct {
	Target string
	Status HostStatus
}

type HostError struct {
	Host    string
	Code    string
	Message string
}

type documentV1 struct {
	SchemaVersion int           `json:"schema_version"`
	Hosts         []hostV1      `json:"hosts"`
	Sessions      []sessionV1   `json:"sessions"`
	Errors        []hostErrorV1 `json:"errors"`
}

type hostV1 struct {
	Target string     `json:"target"`
	Status HostStatus `json:"status"`
}

type sessionV1 struct {
	Host      string `json:"host"`
	Provider  string `json:"provider"`
	NativeID  string `json:"native_id"`
	UpdatedAt string `json:"updated_at"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
}

type hostErrorV1 struct {
	Host    string `json:"host"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func WriteJSON(writer io.Writer, hosts []HostResult, sessions []session.Session, hostErrors []HostError) error {
	document := documentV1{
		SchemaVersion: 1,
		Hosts:         make([]hostV1, 0, len(hosts)),
		Sessions:      make([]sessionV1, 0, len(sessions)),
		Errors:        make([]hostErrorV1, 0, len(hostErrors)),
	}
	for _, host := range hosts {
		document.Hosts = append(document.Hosts, hostV1{Target: host.Target, Status: host.Status})
	}
	for _, item := range sessions {
		document.Sessions = append(document.Sessions, sessionV1{
			Host:      item.Host,
			Provider:  string(item.Provider),
			NativeID:  item.NativeID,
			UpdatedAt: item.UpdatedAt.Format(time.RFC3339Nano),
			CWD:       item.CWD,
			Title:     item.Title,
		})
	}
	for _, hostError := range hostErrors {
		document.Errors = append(document.Errors, hostErrorV1{
			Host: hostError.Host, Code: hostError.Code, Message: hostError.Message,
		})
	}

	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("write JSON output: %w", err)
	}
	return nil
}
