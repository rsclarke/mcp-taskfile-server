package taskfileserver

import (
	"maps"

	"github.com/rsclarke/mcp-taskfile-server/internal/tools"
)

// syncTools snapshots state under lock, builds a plan without the lock,
// then re-acquires the lock to validate the generation and apply changes.
// If the generation has advanced while the lock was released (another
// mutator ran concurrently), the stale plan is discarded without touching
// the MCP server, because that mutator will produce its own sync.
func (s *Server) syncTools() error {
	// Phase 1: snapshot under lock.
	s.mu.Lock()
	snap := s.snapshotToolStateLocked()
	oldTools := make(map[string]tools.RegisteredTool, len(s.registeredTools))
	maps.Copy(oldTools, s.registeredTools)
	s.mu.Unlock()

	// Phase 2: pure planning — no lock held.
	plan := tools.BuildPlan(snap, s.log())
	stale, added := tools.Diff(oldTools, plan.Tools)

	// Phase 3: validate generation, apply MCP side effects, and commit
	// bookkeeping — all under lock to prevent orphaned registrations.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != snap.Generation {
		return nil
	}
	if len(stale) > 0 {
		s.toolRegistry.RemoveTools(stale...)
	}
	for _, name := range added {
		t := plan.Tools[name]
		s.toolRegistry.AddTool(&t.Tool, plan.Handlers[name])
	}
	s.registeredTools = plan.Tools
	return nil
}
