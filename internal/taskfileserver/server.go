package taskfileserver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New creates a new Taskfile MCP server.
func New() *Server {
	return &Server{
		roots:           make(map[string]*Root),
		registeredTools: make(map[string]mcp.Tool),
	}
}

// SetMCPServer attaches the live MCP server instance used for tool updates.
func (s *Server) SetMCPServer(server *mcp.Server) {
	s.mcpServer = server
}

// isMethodNotFound reports whether err is a JSON-RPC "method not found" error,
// which indicates the client does not support the requested capability.
func isMethodNotFound(err error) bool {
	var wireErr *jsonrpc.Error
	return errors.As(err, &wireErr) && wireErr.Code == jsonrpc.CodeMethodNotFound
}

// loadRootsFromSession queries the client for its root list and loads each
// one. If the client does not support roots, it falls back to os.Getwd().
func (s *Server) loadRootsFromSession(ctx context.Context, session *mcp.ServerSession) error {
	rootRes, err := session.ListRoots(ctx, nil)
	if err != nil {
		if !isMethodNotFound(err) {
			return fmt.Errorf("failed to list roots: %w", err)
		}
		// Client does not support roots; fall back to working directory.
		workdir, wdErr := os.Getwd()
		if wdErr != nil {
			return fmt.Errorf("failed to get working directory: %w", wdErr)
		}
		uri := dirToURI(workdir)
		root, loadErr := loadRoot(ctx, workdir)
		if loadErr != nil {
			return loadErr
		}

		s.mu.Lock()
		s.roots[uri] = root
		syncErr := s.syncTools()
		s.mu.Unlock()
		if syncErr != nil {
			return syncErr
		}
		s.restartWatchers(ctx)
		return nil
	}

	s.mu.Lock()
	existing := make(map[string]struct{}, len(s.roots))
	for uri := range s.roots {
		existing[uri] = struct{}{}
	}
	s.mu.Unlock()

	loadedRoots := make(map[string]*Root, len(rootRes.Roots))
	for _, r := range rootRes.Roots {
		canonicalURI, dir, parseErr := canonicalRootURI(r.URI)
		if parseErr != nil {
			log.Printf("skipping root with invalid URI %q: %v", r.URI, parseErr)
			continue
		}
		if _, exists := existing[canonicalURI]; exists {
			continue
		}
		if _, exists := loadedRoots[canonicalURI]; exists {
			continue
		}
		root, loadErr := loadRoot(ctx, dir)
		if loadErr != nil {
			log.Printf("failed to load root %q: %v", r.URI, loadErr)
			continue
		}
		loadedRoots[canonicalURI] = root
	}

	s.mu.Lock()
	for uri, root := range loadedRoots {
		if _, exists := s.roots[uri]; exists {
			continue
		}
		s.roots[uri] = root
	}
	if len(s.roots) == 0 {
		s.mu.Unlock()
		return errors.New("no valid roots found")
	}

	syncErr := s.syncTools()
	s.mu.Unlock()
	if syncErr != nil {
		return syncErr
	}
	s.restartWatchers(ctx)
	return nil
}

// HandleInitialized is called after the client handshake completes.
func (s *Server) HandleInitialized(ctx context.Context, req *mcp.InitializedRequest) {
	if err := s.loadRootsFromSession(ctx, req.Session); err != nil {
		log.Printf("failed to initialize roots: %v", err)
	}
}

// HandleRootsChanged is called when the client sends roots/list_changed.
func (s *Server) HandleRootsChanged(ctx context.Context, req *mcp.RootsListChangedRequest) {
	rootRes, err := req.Session.ListRoots(ctx, nil)
	if err != nil {
		log.Printf("failed to list roots after change: %v", err)
		return
	}

	// Build a set of new canonical root identities.
	newURIs := make(map[string]struct{}, len(rootRes.Roots))

	s.mu.Lock()
	existing := make(map[string]struct{}, len(s.roots))
	for uri := range s.roots {
		existing[uri] = struct{}{}
	}
	s.mu.Unlock()

	loadedRoots := make(map[string]*Root)
	for _, r := range rootRes.Roots {
		canonicalURI, dir, parseErr := canonicalRootURI(r.URI)
		if parseErr != nil {
			log.Printf("skipping root with invalid URI %q: %v", r.URI, parseErr)
			continue
		}
		newURIs[canonicalURI] = struct{}{}
		if _, exists := existing[canonicalURI]; exists {
			continue
		}
		if _, exists := loadedRoots[canonicalURI]; exists {
			continue
		}
		root, loadErr := loadRoot(ctx, dir)
		if loadErr != nil {
			log.Printf("failed to load root %q: %v", r.URI, loadErr)
			continue
		}
		loadedRoots[canonicalURI] = root
	}

	s.mu.Lock()

	// Remove roots that are no longer present.
	for uri := range s.roots {
		if _, ok := newURIs[uri]; !ok {
			s.unloadRoot(uri)
		}
	}

	// Add roots that are new.
	for uri, root := range loadedRoots {
		if _, exists := s.roots[uri]; exists {
			continue
		}
		s.roots[uri] = root
	}

	syncErr := s.syncTools()
	s.mu.Unlock()
	if syncErr != nil {
		log.Printf("failed to sync tools after roots change: %v", syncErr)
	}
	s.restartWatchers(ctx)
}
