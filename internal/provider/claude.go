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

func (adapter claudeAdapter) discover(ctx context.Context, home string, sessionLimit int) Result {
	result := Result{Provider: adapter.Name()}
	if _, err := exec.LookPath("claude"); err != nil {
		result.Status = Absent
		return result
	}

	root := filepath.Join(home, ".claude", "projects")
	if info, err := os.Lstat(root); os.IsNotExist(err) {
		result.Status = Absent
		return result
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return finishResult(result, nil, "unavailable")
	}

	candidates := make(map[string]session.Candidate)
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
			if !isRegularFile(historyPath, entry) {
				return nil
			}

			result.Seen++
			candidate, include, issue := adapter.readHistory(historyPath)
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
			errorCode = strongerError(errorCode, "unavailable")
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return nil
	})
	if os.IsNotExist(err) {
		result.Status = Absent
		return result
	}
	if err != nil {
		errorCode = strongerError(errorCode, "unavailable")
	}
	return finishResult(result, candidates, errorCode)
}

type claudeRecord struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	Title       string `json:"title"`
	CustomTitle string `json:"customTitle"`
	AgentName   string `json:"agentName"`
	AgentID     string `json:"agentId"`
	IsInternal  bool   `json:"isInternal"`
	IsSidechain bool   `json:"isSidechain"`
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

	var id, cwd, title string
	titleRank := 0
	excluded := false
	mixedIDs := false
	errorCode := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxProviderLineBytes)
	for scanner.Scan() {
		var record claudeRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			errorCode = strongerError(errorCode, "corrupt")
			continue
		}
		if record.IsInternal || record.IsSidechain || record.AgentID != "" || record.Type == "internal" {
			excluded = true
		}
		if record.SessionID != "" {
			if err := adapter.ValidateID(record.SessionID); err != nil {
				errorCode = strongerError(errorCode, "incompatible")
				continue
			} else if id == "" {
				id = record.SessionID
			} else if id != record.SessionID {
				mixedIDs = true
				errorCode = strongerError(errorCode, "incompatible")
				continue
			}
		}
		if record.CWD != "" {
			if validClaudeCandidateText(record.CWD, "") {
				cwd = record.CWD
			} else {
				errorCode = strongerError(errorCode, "incompatible")
			}
		}
		value, rank := claudeNativeTitle(record)
		if rank >= titleRank && value != "" && validClaudeCandidateText("/", value) {
			title = value
			titleRank = rank
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
		Title:     title,
	}
	if err := session.ValidateCandidate(candidate); err != nil {
		return session.Candidate{}, false, strongerError(errorCode, "incompatible")
	}
	return candidate, true, errorCode
}

func claudeNativeTitle(record claudeRecord) (string, int) {
	if record.CustomTitle != "" {
		return record.CustomTitle, 3
	}
	switch record.Type {
	case "custom-title":
		return record.Title, 3
	case "ai-title":
		return record.Title, 2
	case "agent-name":
		return firstNonEmpty(record.AgentName, record.Title), 1
	default:
		return "", 0
	}
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
