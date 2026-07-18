package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

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
	result := Result{Provider: adapter.Name()}
	if _, err := exec.LookPath("codex"); err != nil {
		result.Status = Absent
		return result
	}

	root := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Stat(root); os.IsNotExist(err) {
		result.Status = Absent
		return result
	} else if err != nil || !info.IsDir() {
		return finishResult(result, nil, "unavailable")
	}

	candidates := make(map[string]session.Candidate)
	errorCode := ""
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			errorCode = strongerError(errorCode, "unavailable")
			return nil
		}
		if ctx.Err() != nil {
			errorCode = strongerError(errorCode, "unavailable")
			return filepath.SkipAll
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".jsonl" {
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
		newerCandidate(candidates, candidate)
		return nil
	})
	return finishResult(result, candidates, errorCode)
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

	var meta *codexSessionMeta
	errorCode := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxProviderLineBytes)
	for scanner.Scan() {
		var envelope codexEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			errorCode = strongerError(errorCode, "corrupt")
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
			errorCode = strongerError(errorCode, "incompatible")
			continue
		}
		meta = &decoded
	}
	if err := scanner.Err(); err != nil {
		return session.Candidate{}, false, "resource_limit"
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
		Title:     "",
	}
	if err := session.ValidateCandidate(candidate); err != nil {
		return session.Candidate{}, false, strongerError(errorCode, "incompatible")
	}
	return candidate, true, errorCode
}
