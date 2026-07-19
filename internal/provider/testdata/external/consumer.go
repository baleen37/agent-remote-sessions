package external

import (
	"github.com/baleen37/agent-remote-sessions/internal/provider"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func lookupClaude() bool {
	adapter, ok := provider.Lookup(session.Claude)
	return ok && adapter.Name() == session.Claude
}
