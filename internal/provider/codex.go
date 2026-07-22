package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const maxCodexSessionDepth = 64

var errCodexSessionDepth = errors.New("Codex session traversal exceeds maximum depth")

type codexAdapter struct{}

func (codexAdapter) Name() session.Provider { return session.Codex }

func (adapter codexAdapter) ValidateID(id string) error {
	return validateID(adapter.Name(), id)
}

func (adapter codexAdapter) Resume(id string) (ResumeSpec, error) {
	if err := adapter.ValidateID(id); err != nil {
		return ResumeSpec{}, err
	}
	return ResumeSpec{Executable: "codex", Args: []string{"resume", id}}, nil
}

func (adapter codexAdapter) Discover(ctx context.Context, home string) Result {
	return adapter.discover(ctx, home, maxDiscoveredSessions)
}

func (adapter codexAdapter) discover(ctx context.Context, home string, sessionLimit int) Result {
	result := Result{Provider: adapter.Name()}
	if _, err := exec.LookPath("codex"); err != nil {
		result.Status = Absent
		return result
	}

	root := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Lstat(root); os.IsNotExist(err) {
		result.Status = Absent
		return result
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return finishResult(result, nil, "unavailable")
	}

	candidates := make(map[string]session.Candidate)
	errorCode := ""
	err := walkCodexSessionDirectory(ctx, root, 0, func(path string, entry os.DirEntry) error {
		if filepath.Ext(entry.Name()) != ".jsonl" || !isRegularFile(path, entry) {
			return nil
		}

		result.Seen++
		candidate, include, issue := adapter.readHistory(path)
		if issue != "" {
			errorCode = strongerError(errorCode, issue)
		}
		if !include {
			result.Skipped++
			return nil
		}
		if !newerCandidate(candidates, candidate, sessionLimit) {
			result.Skipped++
			errorCode = strongerError(errorCode, "resource_limit")
		}
		return nil
	})
	if err != nil {
		issue := "unavailable"
		if errors.Is(err, errCodexSessionDepth) {
			issue = "resource_limit"
		}
		errorCode = strongerError(errorCode, issue)
	}
	return finishResult(result, candidates, errorCode)
}

func walkCodexSessionDirectory(ctx context.Context, directory string, depth int, visit func(string, os.DirEntry) error) error {
	return readDirBatches(ctx, directory, func(entry os.DirEntry) error {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		path := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			if depth >= maxCodexSessionDepth {
				return errCodexSessionDepth
			}
			return walkCodexSessionDirectory(ctx, path, depth+1, visit)
		}
		return visit(path, entry)
	})
}

type codexEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	ID           string `json:"id"`
	CWD          string `json:"cwd"`
	Source       string `json:"source"`
	ThreadSource string `json:"thread_source"`
}

type codexEventMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (adapter codexAdapter) readHistory(path string) (session.Candidate, bool, string) {
	file, err := os.Open(path)
	if err != nil {
		return session.Candidate{}, false, "unavailable"
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return session.Candidate{}, false, "unavailable"
	}
	if !info.Mode().IsRegular() {
		return session.Candidate{}, false, "incompatible"
	}

	var meta *codexSessionMeta
	title := ""
	multipleMeta := false
	errorCode := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxProviderLineBytes)
	for scanner.Scan() {
		var envelope codexEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			errorCode = strongerError(errorCode, "corrupt")
			continue
		}
		if envelope.Type == "event_msg" && title == "" && len(envelope.Payload) > 0 {
			var event codexEventMsg
			if json.Unmarshal(envelope.Payload, &event) == nil && event.Type == "user_message" {
				title = codexTitle(event.Message)
			}
			continue
		}
		if envelope.Type != "session_meta" {
			continue
		}
		var decoded codexSessionMeta
		if len(envelope.Payload) == 0 || json.Unmarshal(envelope.Payload, &decoded) != nil {
			errorCode = strongerError(errorCode, "corrupt")
			continue
		}
		if meta != nil {
			multipleMeta = true
			errorCode = strongerError(errorCode, "incompatible")
			continue
		}
		meta = &decoded
	}
	if err := scanner.Err(); err != nil {
		return session.Candidate{}, false, "resource_limit"
	}
	if multipleMeta {
		return session.Candidate{}, false, "incompatible"
	}
	if meta == nil {
		if errorCode == "" {
			errorCode = "incompatible"
		}
		return session.Candidate{}, false, errorCode
	}
	if meta.ThreadSource != "user" || (meta.Source != "cli" && meta.Source != "vscode") {
		return session.Candidate{}, false, errorCode
	}

	candidate := session.Candidate{
		Provider:  adapter.Name(),
		NativeID:  meta.ID,
		UpdatedAt: info.ModTime().UTC(),
		CWD:       meta.CWD,
		Title:     title,
	}
	if err := session.ValidateCandidate(candidate); err != nil {
		return session.Candidate{}, false, strongerError(errorCode, "incompatible")
	}
	return candidate, true, errorCode
}

// codexTitle turns the first user message into a display title that always
// satisfies candidate text validation: single line, no control runes, at most
// MaxTitleBytes bytes.
func codexTitle(message string) string {
	if index := strings.IndexByte(message, '\n'); index >= 0 {
		message = message[:index]
	}
	message = strings.Join(strings.FieldsFunc(message, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	for len(message) > session.MaxTitleBytes {
		_, size := utf8.DecodeLastRuneInString(message)
		message = message[:len(message)-size]
	}
	return strings.TrimSpace(message)
}
