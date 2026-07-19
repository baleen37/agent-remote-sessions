package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

const (
	directoryBatchSize    = 128
	maxDiscoveredSessions = 10_000
)

type Status string

const (
	Absent  Status = "absent"
	OK      Status = "ok"
	Partial Status = "partial"
	Error   Status = "error"
)

type Result struct {
	Provider  session.Provider
	Sessions  []session.Candidate
	Status    Status
	Seen      int
	Skipped   int
	ErrorCode string
}

type ResumeSpec struct {
	Executable string
	Args       []string
}

type Adapter interface {
	Name() session.Provider
	Discover(context.Context, string) Result
	ValidateID(string) error
	Resume(string) (ResumeSpec, error)
}

func Builtin() []Adapter {
	return []Adapter{claudeAdapter{}, codexAdapter{}}
}

func Lookup(name session.Provider) (Adapter, bool) {
	for _, adapter := range Builtin() {
		if adapter.Name() == name {
			return adapter, true
		}
	}
	return nil, false
}

func validateID(provider session.Provider, id string) error {
	candidate := session.Candidate{
		Provider:  provider,
		NativeID:  id,
		UpdatedAt: time.Unix(1, 0),
		CWD:       "/",
	}
	if err := session.ValidateCandidate(candidate); err != nil {
		return fmt.Errorf("invalid %s session ID: %w", provider, err)
	}
	return nil
}

func finishResult(result Result, candidates map[string]session.Candidate, errorCode string) Result {
	result.Sessions = make([]session.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		result.Sessions = append(result.Sessions, candidate)
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		return result.Sessions[i].NativeID < result.Sessions[j].NativeID
	})

	if result.Seen == 0 && errorCode == "" {
		result.Status = Absent
		return result
	}
	if errorCode == "" {
		result.Status = OK
		return result
	}
	result.ErrorCode = errorCode
	if len(result.Sessions) > 0 {
		result.Status = Partial
	} else {
		result.Status = Error
	}
	return result
}

func strongerError(current, next string) string {
	priority := map[string]int{
		"unavailable":    1,
		"corrupt":        2,
		"incompatible":   3,
		"resource_limit": 4,
	}
	if priority[next] > priority[current] {
		return next
	}
	return current
}

func newerCandidate(candidates map[string]session.Candidate, candidate session.Candidate, limit int) bool {
	current, exists := candidates[candidate.NativeID]
	if exists {
		if candidate.UpdatedAt.After(current.UpdatedAt) {
			candidates[candidate.NativeID] = candidate
		}
		return true
	}
	if len(candidates) >= limit {
		return false
	}
	candidates[candidate.NativeID] = candidate
	return true
}

func readDirBatches(ctx context.Context, directory string, visit func(os.DirEntry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := handle.ReadDir(directoryBatchSize)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func isRegularFile(path string, entry os.DirEntry) bool {
	if entry.Type()&os.ModeSymlink != 0 {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
