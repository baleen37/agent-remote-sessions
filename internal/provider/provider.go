package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
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

func ValidResumeSpec(name session.Provider, id string, spec ResumeSpec) bool {
	switch name {
	case session.Claude:
		return spec.Executable == "claude" && slices.Equal(spec.Args, []string{"--resume", id})
	case session.Codex:
		return spec.Executable == "codex" && slices.Equal(spec.Args, []string{"resume", id})
	default:
		return false
	}
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

func DiscoverAll(ctx context.Context, home string, adapters []Adapter) ([]session.Candidate, []Result, error) {
	if !validRegistry(adapters) {
		return nil, nil, fmt.Errorf("invalid provider registry")
	}

	results := make([]Result, 0, len(adapters))
	candidates := make([]session.Candidate, 0)
	for _, adapter := range adapters {
		result := adapter.Discover(ctx, home)
		if result.Provider != adapter.Name() {
			return nil, nil, fmt.Errorf("invalid provider result")
		}
		for _, candidate := range result.Sessions {
			if candidate.Provider != result.Provider || session.ValidateCandidate(candidate) != nil {
				return nil, nil, fmt.Errorf("invalid provider candidate")
			}
			if len(candidates) >= maxDiscoveredSessions {
				return nil, nil, fmt.Errorf("combined session count exceeds limit")
			}
			candidates = append(candidates, candidate)
		}
		results = append(results, result)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Provider != candidates[j].Provider {
			return providerOrder(candidates[i].Provider) < providerOrder(candidates[j].Provider)
		}
		return candidates[i].NativeID < candidates[j].NativeID
	})
	sort.Slice(results, func(i, j int) bool {
		return providerOrder(results[i].Provider) < providerOrder(results[j].Provider)
	})
	return candidates, results, nil
}

func validRegistry(adapters []Adapter) bool {
	if len(adapters) != 2 {
		return false
	}
	seen := make(map[session.Provider]struct{}, len(adapters))
	for _, adapter := range adapters {
		if adapter == nil || (adapter.Name() != session.Claude && adapter.Name() != session.Codex) {
			return false
		}
		if _, exists := seen[adapter.Name()]; exists {
			return false
		}
		seen[adapter.Name()] = struct{}{}
	}
	return true
}

func providerOrder(name session.Provider) int {
	if name == session.Claude {
		return 0
	}
	return 1
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
