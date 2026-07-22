package app

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/runtime"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

type Result struct {
	Hosts    []output.HostResult
	Sessions []session.Session
	Errors   []output.HostError
	Warnings []output.HostError
}

type Collector func(context.Context, Host) (
	[]session.Discovered,
	[]provider.Result,
	runtime.Report,
	error,
)

type hostCollection struct {
	host     output.HostResult
	sessions []session.Session
	err      *output.HostError
	warnings []output.HostError
}

func CollectHosts(ctx context.Context, hosts []Host, workerLimit int, collector Collector) Result {
	var last Snapshot
	CollectHostsStream(ctx, hosts, workerLimit, collector, HostCache{}, func(update Snapshot) {
		last = update
	})
	return last.Result
}

func collectHost(ctx context.Context, host Host, collector Collector) hostCollection {
	if err := validateTarget(host.Target); err != nil {
		return failedCollection(host.Target, "unsupported_target", "SSH target is unsupported")
	}
	discovered, providerResults, report, err := collector(ctx, host)
	if err != nil {
		code, message := classifyCollectionError(ctx, err)
		return failedCollection(host.Target, code, message)
	}

	warnings, err := providerWarnings(host.Target, providerResults)
	if err != nil {
		return failedCollection(host.Target, "protocol_error", "Collector protocol failed")
	}
	if err := validateRuntimeSessions(discovered, report); err != nil {
		return failedCollection(host.Target, "protocol_error", "Collector protocol failed")
	}
	runtimeDiagnostic, err := runtimeWarning(host.Target, report)
	if err != nil {
		return failedCollection(host.Target, "protocol_error", "Collector protocol failed")
	}
	if runtimeDiagnostic != nil {
		warnings = append(warnings, *runtimeDiagnostic)
	}
	sessions := make([]session.Session, 0, len(discovered))
	for _, item := range discovered {
		bound, err := session.BindDiscovered(host.Target, item)
		if err != nil {
			return failedCollection(host.Target, "protocol_error", "Collector protocol failed")
		}
		sessions = append(sessions, bound)
	}
	return hostCollection{
		host:     output.HostResult{Target: host.Target, Status: output.HostOK},
		sessions: sessions,
		warnings: warnings,
	}
}

func providerWarnings(target string, results []provider.Result) ([]output.HostError, error) {
	warnings := make([]output.HostError, 0, len(results))
	for _, result := range results {
		var label string
		switch result.Provider {
		case session.Claude:
			label = "Claude"
		case session.Codex:
			label = "Codex"
		default:
			return nil, errors.New("invalid provider result")
		}

		var state string
		switch result.Status {
		case provider.Absent, provider.OK:
			if result.ErrorCode != "" {
				return nil, errors.New("invalid provider result")
			}
			continue
		case provider.Partial:
			state = "partial"
		case provider.Error:
			state = "failed"
		default:
			return nil, errors.New("invalid provider result")
		}
		if !validProviderErrorCode(result.ErrorCode) {
			return nil, errors.New("invalid provider result")
		}
		warnings = append(warnings, output.HostError{
			Host: target, Code: result.ErrorCode, Message: label + " discovery " + state,
		})
	}
	return warnings, nil
}

func validProviderErrorCode(code string) bool {
	switch code {
	case "unavailable", "incompatible", "corrupt", "resource_limit":
		return true
	default:
		return false
	}
}

func validateRuntimeSessions(discovered []session.Discovered, report runtime.Report) error {
	if report.Status == runtime.StatusOK {
		return nil
	}
	for _, item := range discovered {
		if item.Runtime.State != session.RuntimeSaved {
			return errors.New("runtime report conflicts with session state")
		}
	}
	return nil
}

func runtimeWarning(target string, report runtime.Report) (*output.HostError, error) {
	switch report.Status {
	case runtime.StatusOK:
		if report.ErrorCode != "" {
			return nil, errors.New("invalid runtime report")
		}
		return nil, nil
	case runtime.StatusUnavailable:
		if report.ErrorCode != "tmux_unavailable" {
			return nil, errors.New("invalid runtime report")
		}
		warning := output.HostError{Host: target, Code: report.ErrorCode, Message: "Runtime inspection unavailable"}
		return &warning, nil
	case runtime.StatusFailed:
		if report.ErrorCode != "tmux_failed" {
			return nil, errors.New("invalid runtime report")
		}
		warning := output.HostError{Host: target, Code: report.ErrorCode, Message: "Runtime inspection failed"}
		return &warning, nil
	default:
		return nil, errors.New("invalid runtime report")
	}
}

func failedCollection(target, code, message string) hostCollection {
	hostError := output.HostError{Host: target, Code: code, Message: message}
	return hostCollection{
		host: output.HostResult{Target: target, Status: output.HostStatusError},
		err:  &hostError,
	}
}

func classifyCollectionError(ctx context.Context, err error) (string, string) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "ssh_timeout", "SSH collection timed out"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unsupported ssh target"),
		strings.Contains(message, "unsupported collector target"):
		return "unsupported_target", "SSH target is unsupported"
	case strings.Contains(message, "resource_limit"),
		strings.Contains(message, "exceeds limit"),
		strings.Contains(message, "limit exceeded"):
		return "resource_limit", "Collector resource limit exceeded"
	case strings.Contains(message, "protocol"),
		strings.Contains(message, "invalid ssh target probe output"),
		strings.Contains(message, "invalid remote temporary path"):
		return "protocol_error", "Collector protocol failed"
	default:
		return "ssh_failed", "SSH collection failed"
	}
}

func mergeCollections(collections []hostCollection) Result {
	result := Result{
		Hosts:    make([]output.HostResult, 0, len(collections)),
		Errors:   make([]output.HostError, 0),
		Warnings: make([]output.HostError, 0),
	}
	deduplicated := make(map[sessionIdentity]session.Session)
	for _, collection := range collections {
		result.Hosts = append(result.Hosts, collection.host)
		if collection.err != nil {
			result.Errors = append(result.Errors, *collection.err)
		}
		result.Warnings = append(result.Warnings, collection.warnings...)
		for _, item := range collection.sessions {
			identity := sessionIdentity{host: item.Host, provider: item.Provider, nativeID: item.NativeID}
			current, exists := deduplicated[identity]
			if !exists || preferredDuplicate(item, current) {
				deduplicated[identity] = item
			}
		}
	}
	result.Sessions = make([]session.Session, 0, len(deduplicated))
	for _, item := range deduplicated {
		result.Sessions = append(result.Sessions, item)
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		return sessionLess(result.Sessions[i], result.Sessions[j])
	})
	return result
}

type sessionIdentity struct {
	host     string
	provider session.Provider
	nativeID string
}

func preferredDuplicate(candidate, current session.Session) bool {
	if !candidate.UpdatedAt.Equal(current.UpdatedAt) {
		return candidate.UpdatedAt.After(current.UpdatedAt)
	}
	if candidate.CWD != current.CWD {
		return candidate.CWD < current.CWD
	}
	return candidate.Title < current.Title
}

func sessionLess(left, right session.Session) bool {
	if !left.UpdatedAt.Equal(right.UpdatedAt) {
		return left.UpdatedAt.After(right.UpdatedAt)
	}
	if left.Host != right.Host {
		return left.Host < right.Host
	}
	if left.Provider != right.Provider {
		return left.Provider < right.Provider
	}
	if left.NativeID != right.NativeID {
		return left.NativeID < right.NativeID
	}
	if left.CWD != right.CWD {
		return left.CWD < right.CWD
	}
	return left.Title < right.Title
}
