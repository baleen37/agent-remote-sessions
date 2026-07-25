package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

func (adapter codexAdapter) DiscoverStream(
	ctx context.Context,
	home string,
	recentAfter time.Time,
	emit func(Phase, Result) error,
) error {
	return adapter.discoverStream(ctx, home, recentAfter, maxDiscoveredSessions, emit)
}

func (adapter codexAdapter) discover(ctx context.Context, home string, sessionLimit int) Result {
	var final Result
	err := adapter.discoverStream(ctx, home, time.Time{}, sessionLimit, func(phase Phase, result Result) error {
		if phase == PhaseComplete {
			final = result
		}
		return nil
	})
	if err != nil {
		return finishResult(Result{Provider: adapter.Name()}, nil, "unavailable")
	}
	return final
}

func (adapter codexAdapter) discoverStream(
	ctx context.Context,
	home string,
	recentAfter time.Time,
	sessionLimit int,
	emit func(Phase, Result) error,
) error {
	files, inventoryIssue, err := adapter.historyFiles(ctx, home)
	if err != nil {
		return err
	}
	scanBuffer := make([]byte, maxProviderLineBytes)
	return discoverHistoryStream(
		ctx,
		adapter.Name(),
		files,
		inventoryIssue,
		recentAfter,
		sessionLimit,
		func(path string) (session.Candidate, bool, string) {
			return adapter.readHistoryBuffer(path, scanBuffer)
		},
		emit,
	)
}

func (adapter codexAdapter) historyFiles(ctx context.Context, home string) ([]historyFile, string, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return nil, "", nil
	}

	root := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Lstat(root); os.IsNotExist(err) {
		return nil, "", nil
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "unavailable", nil
	}

	var files []historyFile
	errorCode := ""
	err := walkCodexSessionDirectory(ctx, root, 0, func(path string, entry os.DirEntry) error {
		if filepath.Ext(entry.Name()) != ".jsonl" {
			return nil
		}
		file, ok := historyFileForEntry(path, entry)
		if !ok {
			return nil
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		issue := "unavailable"
		if errors.Is(err, errCodexSessionDepth) {
			issue = "resource_limit"
		}
		errorCode = strongerError(errorCode, issue)
	}
	return files, errorCode, nil
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

type codexHeader struct {
	Type string `json:"type"`
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
	return adapter.readHistoryBuffer(path, make([]byte, 64*1024))
}

func (adapter codexAdapter) readHistoryBuffer(path string, scanBuffer []byte) (session.Candidate, bool, string) {
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
	scanner := newHistoryScanner(file, scanBuffer)
	for scanner.Scan() {
		line := scanner.Bytes()
		var header codexHeader
		if err := json.Unmarshal(line, &header); err != nil {
			errorCode = strongerError(errorCode, "corrupt")
			continue
		}
		if header.Type == "event_msg" && title == "" {
			var envelope codexEnvelope
			if json.Unmarshal(line, &envelope) != nil {
				errorCode = strongerError(errorCode, "corrupt")
				continue
			}
			var event codexEventMsg
			if len(envelope.Payload) > 0 && json.Unmarshal(envelope.Payload, &event) == nil && event.Type == "user_message" {
				title = codexTitle(event.Message)
			}
			continue
		}
		if header.Type != "session_meta" {
			continue
		}
		var envelope codexEnvelope
		if json.Unmarshal(line, &envelope) != nil {
			errorCode = strongerError(errorCode, "corrupt")
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
	if candidate.Title != "" && session.ValidateCandidate(candidate) != nil {
		candidate.Title = ""
	}
	if err := session.ValidateCandidate(candidate); err != nil {
		return session.Candidate{}, false, strongerError(errorCode, "incompatible")
	}
	return candidate, true, errorCode
}

// codexTitle turns the first non-empty line of the first user message into a display title that always
// satisfies candidate text validation: single line, no control runes, at most
// MaxTitleBytes bytes.
func codexTitle(message string) string {
	for _, line := range strings.Split(message, "\n") {
		line = strings.Join(strings.FieldsFunc(line, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}), " ")
		if line == "" {
			continue
		}
		for len(line) > session.MaxTitleBytes {
			_, size := utf8.DecodeLastRuneInString(line)
			line = line[:len(line)-size]
		}
		return strings.TrimSpace(line)
	}
	return ""
}
