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
	result := Result{Provider: adapter.Name()}
	if _, err := exec.LookPath("claude"); err != nil {
		result.Status = Absent
		return result
	}

	root := filepath.Join(home, ".claude", "projects")
	projects, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		result.Status = Absent
		return result
	}
	if err != nil {
		return finishResult(result, nil, "unavailable")
	}

	candidates := make(map[string]session.Candidate)
	errorCode := ""
	for _, project := range projects {
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, project.Name()))
		if err != nil {
			errorCode = strongerError(errorCode, "unavailable")
			continue
		}
		for _, entry := range files {
			if ctx.Err() != nil {
				errorCode = strongerError(errorCode, "unavailable")
				break
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".jsonl" {
				continue
			}

			result.Seen++
			path := filepath.Join(root, project.Name(), entry.Name())
			candidate, include, issue := adapter.readHistory(path)
			if issue != "" {
				errorCode = strongerError(errorCode, issue)
			}
			if !include {
				result.Skipped++
				continue
			}
			newerCandidate(candidates, candidate)
		}
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

	var id, cwd, title string
	titleRank := 0
	excluded := false
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
			} else if id == "" {
				id = record.SessionID
			} else if id != record.SessionID {
				errorCode = strongerError(errorCode, "incompatible")
			}
		}
		if record.CWD != "" {
			if validCandidateText(adapter.Name(), firstNonEmpty(id, canonicalID), record.CWD, "") {
				cwd = record.CWD
			} else {
				errorCode = strongerError(errorCode, "incompatible")
			}
		}
		value, rank := claudeNativeTitle(record)
		if rank >= titleRank && value != "" && validCandidateText(adapter.Name(), firstNonEmpty(id, canonicalID), "/", value) {
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

func validCandidateText(provider session.Provider, id, cwd, title string) bool {
	return session.ValidateCandidate(session.Candidate{
		Provider:  provider,
		NativeID:  id,
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
