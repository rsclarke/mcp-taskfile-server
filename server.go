package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewTaskfileServer creates a new Taskfile MCP server.
func NewTaskfileServer() *TaskfileServer {
	return &TaskfileServer{
		roots: make(map[string]*rootState),
	}
}

// isMethodNotFound reports whether err is a JSON-RPC "method not found" error,
// which indicates the client does not support the requested capability.
func isMethodNotFound(err error) bool {
	var wireErr *jsonrpc.Error
	return errors.As(err, &wireErr) && wireErr.Code == jsonrpc.CodeMethodNotFound
}

// loadRootsFromSession queries the client for its root list and loads each
// one. If the client does not support roots, it falls back to os.Getwd().
func (s *TaskfileServer) loadRootsFromSession(ctx context.Context, session *mcp.ServerSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
		s.roots[uri] = root
		if err := s.syncTools(); err != nil {
			return err
		}
		s.restartWatchers(ctx)
		return nil
	}

	for _, r := range rootRes.Roots {
		if _, exists := s.roots[r.URI]; exists {
			continue
		}
		dir, parseErr := uriToDir(r.URI)
		if parseErr != nil {
			log.Printf("skipping root with invalid URI %q: %v", r.URI, parseErr)
			continue
		}
		root, loadErr := loadRoot(ctx, dir)
		if loadErr != nil {
			log.Printf("failed to load root %q: %v", r.URI, loadErr)
			continue
		}
		s.roots[r.URI] = root
	}

	if len(s.roots) == 0 {
		return errors.New("no valid roots found")
	}

	if err := s.syncTools(); err != nil {
		return err
	}
	s.restartWatchers(ctx)
	return nil
}

// handleInitialized is called after the client handshake completes.
func (s *TaskfileServer) handleInitialized(ctx context.Context, req *mcp.InitializedRequest) {
	if err := s.loadRootsFromSession(ctx, req.Session); err != nil {
		log.Printf("failed to initialize roots: %v", err)
	}
}

// handleRootsChanged is called when the client sends roots/list_changed.
func (s *TaskfileServer) handleRootsChanged(ctx context.Context, req *mcp.RootsListChangedRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rootRes, err := req.Session.ListRoots(ctx, nil)
	if err != nil {
		log.Printf("failed to list roots after change: %v", err)
		return
	}

	// Build a set of new URIs.
	newURIs := make(map[string]struct{}, len(rootRes.Roots))
	for _, r := range rootRes.Roots {
		newURIs[r.URI] = struct{}{}
	}

	// Remove roots that are no longer present.
	for uri := range s.roots {
		if _, ok := newURIs[uri]; !ok {
			s.unloadRoot(uri)
		}
	}

	// Add roots that are new.
	for _, r := range rootRes.Roots {
		if _, exists := s.roots[r.URI]; exists {
			continue
		}
		dir, parseErr := uriToDir(r.URI)
		if parseErr != nil {
			log.Printf("skipping root with invalid URI %q: %v", r.URI, parseErr)
			continue
		}
		root, loadErr := loadRoot(ctx, dir)
		if loadErr != nil {
			log.Printf("failed to load root %q: %v", r.URI, loadErr)
			continue
		}
		s.roots[r.URI] = root
	}

	if err := s.syncTools(); err != nil {
		log.Printf("failed to sync tools after roots change: %v", err)
	}
	s.restartWatchers(ctx)
}
