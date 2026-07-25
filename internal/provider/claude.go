package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const maxProviderLineBytes = 1 << 20

// candidateTextValidationID is used only for independent CWD/title validation.
const candidateTextValidationID = "00000000-0000-0000-0000-000000000000"

type claudeAdapter struct{}

func (claudeAdapter) Name() session.Provider { return session.Claude }

func (adapter claudeAdapter) ValidateID(id string) error {
	return validateID(adapter.Name(), id)
}

func (adapter claudeAdapter) Resume(id string) (ResumeSpec, error) {
	if err := adapter.ValidateID(id); err != nil {
		return ResumeSpec{}, err
	}
	return ResumeSpec{Executable: "claude", Args: []string{"--resume", id}}, nil
}

func (adapter claudeAdapter) Discover(ctx context.Context, home string) Result {
	return adapter.discover(ctx, home, maxDiscoveredSessions)
}

func (adapter claudeAdapter) DiscoverStream(
	ctx context.Context,
	home string,
	recentAfter time.Time,
	emit func(Phase, Result) error,
) error {
	return adapter.discoverStream(ctx, home, recentAfter, maxDiscoveredSessions, emit)
}

func (adapter claudeAdapter) discover(ctx context.Context, home string, sessionLimit int) Result {
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

func (adapter claudeAdapter) discoverStream(
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
	return discoverHistoryStream(
		ctx,
		adapter.Name(),
		files,
		inventoryIssue,
		recentAfter,
		sessionLimit,
		adapter.readHistory,
		emit,
	)
}

func (adapter claudeAdapter) historyFiles(ctx context.Context, home string) ([]historyFile, string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, "", nil
	}

	root := filepath.Join(home, ".claude", "projects")
	if info, err := os.Lstat(root); os.IsNotExist(err) {
		return nil, "", nil
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "unavailable", nil
	}

	var files []historyFile
	errorCode := ""
	err := readDirBatches(ctx, root, func(project os.DirEntry) error {
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			return nil
		}
		projectDirectory := filepath.Join(root, project.Name())
		err := readDirBatches(ctx, projectDirectory, func(entry os.DirEntry) error {
			if filepath.Ext(entry.Name()) != ".jsonl" {
				return nil
			}
			historyPath := filepath.Join(projectDirectory, entry.Name())
			file, ok := historyFileForEntry(historyPath, entry)
			if !ok {
				return nil
			}
			files = append(files, file)
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			errorCode = strongerError(errorCode, "unavailable")
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		errorCode = strongerError(errorCode, "unavailable")
	}
	return files, errorCode, nil
}

type claudeHeader struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	Title       string `json:"title"`
	CustomTitle string `json:"customTitle"`
	AgentName   string `json:"agentName"`
	AgentID     string `json:"agentId"`
	IsInternal  bool   `json:"isInternal"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
}

type claudeMessage struct {
	Message json.RawMessage `json:"message"`
}

func (adapter claudeAdapter) readHistory(path string) (session.Candidate, bool, string) {
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

	var id, cwd, title, substantialPromptTitle, weakPromptTitle string
	titleRank := 0
	excluded := false
	mixedIDs := false
	errorCode := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxProviderLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		var header claudeHeader
		if err := json.Unmarshal(line, &header); err != nil {
			errorCode = strongerError(errorCode, "corrupt")
			continue
		}
		if header.IsInternal || header.IsSidechain || header.AgentID != "" || header.Type == "internal" {
			excluded = true
		}
		if header.SessionID != "" {
			if err := adapter.ValidateID(header.SessionID); err != nil {
				errorCode = strongerError(errorCode, "incompatible")
				continue
			} else if id == "" {
				id = header.SessionID
			} else if id != header.SessionID {
				mixedIDs = true
				errorCode = strongerError(errorCode, "incompatible")
				continue
			}
		}
		if header.CWD != "" {
			if validClaudeCandidateText(header.CWD, "") {
				cwd = header.CWD
			} else {
				errorCode = strongerError(errorCode, "incompatible")
			}
		}
		value, rank := claudeNativeTitle(header)
		if rank >= titleRank && value != "" && validClaudeCandidateText("/", value) {
			title = value
			titleRank = rank
		}
		if substantialPromptTitle == "" && header.Type == "user" && !header.IsMeta {
			var message claudeMessage
			if json.Unmarshal(line, &message) == nil {
				if candidate := claudePromptTitle(message.Message); candidate != "" {
					if isWeakPromptTitle(candidate) {
						if weakPromptTitle == "" {
							weakPromptTitle = candidate
						}
					} else {
						substantialPromptTitle = candidate
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return session.Candidate{}, false, "resource_limit"
	}
	if excluded {
		return session.Candidate{}, false, errorCode
	}
	if mixedIDs {
		return session.Candidate{}, false, "incompatible"
	}
	if id != "" && cwd == "" && errorCode == "" {
		// Title-only sidecar files carry a session ID and native titles
		// but no transcript records; they are metadata, not a session.
		return session.Candidate{}, false, ""
	}
	if id == "" || cwd == "" {
		if errorCode == "" {
			errorCode = "incompatible"
		}
		return session.Candidate{}, false, errorCode
	}

	candidate := session.Candidate{
		Provider:  adapter.Name(),
		NativeID:  id,
		UpdatedAt: info.ModTime().UTC(),
		CWD:       cwd,
		Title:     firstNonEmpty(title, substantialPromptTitle, weakPromptTitle),
	}
	if err := session.ValidateCandidate(candidate); err != nil {
		return session.Candidate{}, false, strongerError(errorCode, "incompatible")
	}
	return candidate, true, errorCode
}

func claudeNativeTitle(header claudeHeader) (string, int) {
	if header.CustomTitle != "" {
		return header.CustomTitle, 3
	}
	switch header.Type {
	case "custom-title":
		return header.Title, 3
	case "ai-title":
		return header.Title, 2
	case "agent-name":
		return firstNonEmpty(header.AgentName, header.Title), 1
	default:
		return "", 0
	}
}

// claudePromptTitle turns a user transcript record into a display title, so sessions
// without a native title record still show something other than their UUID. Meta
// records, slash-command wrappers, and tool-result-only content carry no prompt the
// user typed, so they yield "" and the caller keeps looking at later records.
func claudePromptTitle(rawMessage json.RawMessage) string {
	if len(rawMessage) == 0 {
		return ""
	}
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(rawMessage, &message) != nil {
		return ""
	}
	text := claudeMessageText(message.Content)
	if text == "" || strings.HasPrefix(text, "<") || strings.HasPrefix(text, "Caveat:") {
		return ""
	}
	title := codexTitle(text)
	if title == "" || !validClaudeCandidateText("/", title) {
		return ""
	}
	return title
}

// isWeakPromptTitle reports whether a prompt title is a throwaway single word (like
// "tes" or "ls") rather than a substantial prompt, so the scan loop can keep looking
// for a better title instead of settling on the first thing the user typed.
func isWeakPromptTitle(title string) bool {
	return !strings.ContainsAny(title, " \t") && len([]rune(title)) < 8
}

// claudeMessageText extracts the prompt text from message.content, which is either a
// plain string or an array of blocks whose first text block holds the prompt.
func claudeMessageText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return strings.TrimSpace(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	for _, block := range blocks {
		if block.Type == "text" {
			return strings.TrimSpace(block.Text)
		}
	}
	return ""
}

func validClaudeCandidateText(cwd, title string) bool {
	return session.ValidateCandidate(session.Candidate{
		Provider:  session.Claude,
		NativeID:  candidateTextValidationID,
		UpdatedAt: time.Unix(1, 0),
		CWD:       cwd,
		Title:     title,
	}) == nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
