package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/model"
)

const (
	maxArtifacts       = 8
	maxArtifactBytes   = bundle.MaxBytes
	maxArtifactStorage = 2 * maxArtifactBytes
)

var (
	errArtifactScope   = errors.New("Bundle provenance or scope does not match the startup policy")
	errArtifactMissing = errors.New("Artifact is unavailable in this server session")
	errArtifactLimit   = errors.New("MCP artifact storage limit reached. Inspect the capture locally or start a new server session")
	errArtifactRead    = errors.New("Could not read a verified MCP evidence bundle")
)

type scopePolicy struct {
	context    string
	namespaces map[string]bool
	cluster    bool
	clusterID  string
}

func (p scopePolicy) permits(scope model.Scope) bool {
	if p.context != scope.Context {
		return false
	}
	switch scope.Kind {
	case model.NamespaceScope:
		return p.namespaces[scope.Namespace]
	case model.ClusterScope:
		return scope.Namespace == "" && p.cluster
	default:
		return false
	}
}

func (p scopePolicy) permitsArtifact(scope model.Scope, clusterID, provenance string) bool {
	if !p.permits(scope) {
		return false
	}
	if p.clusterID == "" {
		return true
	}
	return p.clusterID == clusterID && (provenance == model.KubeSystemUIDProvenance ||
		(scope.Kind == model.NamespaceScope && provenance == model.NamespaceUIDProvenance))
}

type artifactReference struct {
	ArtifactID string      `json:"artifact_id"`
	Scope      model.Scope `json:"scope"`
	CapturedAt time.Time   `json:"captured_at"`
	ClusterID  string      `json:"cluster_id,omitempty"`
	Provenance string      `json:"cluster_provenance,omitempty"`
}

type artifactList struct {
	Artifacts []artifactReference `json:"artifacts"`
}

type artifactEntry struct {
	reference artifactReference
	data      []byte
}

type artifactStore struct {
	policy scopePolicy

	mu      sync.RWMutex
	entries map[string]artifactEntry
	order   []string
	bytes   int
}

func newArtifactStore(policy scopePolicy) *artifactStore {
	return &artifactStore{policy: policy, entries: make(map[string]artifactEntry)}
}

func (s *artifactStore) register(ctx context.Context, evidence *bundle.Bundle) (artifactReference, error) {
	inventory := evidence.Inventory()
	if !s.policy.permitsArtifact(inventory.Scope, inventory.ClusterID, inventory.ClusterProvenance) {
		return artifactReference{}, errArtifactScope
	}
	s.mu.RLock()
	count, remaining := len(s.entries), maxArtifactStorage-s.bytes
	s.mu.RUnlock()
	if count >= maxArtifacts || remaining == 0 {
		return artifactReference{}, errArtifactLimit
	}
	data := &boundedArtifactBuffer{limit: min(remaining, maxArtifactBytes), ctx: ctx}
	if err := evidence.Encode(data); err != nil {
		if data.overflow {
			return artifactReference{}, errArtifactLimit
		}
		if ctx.Err() != nil {
			return artifactReference{}, errOperationTimeout
		}
		return artifactReference{}, errArtifactRead
	}
	if ctx.Err() != nil {
		return artifactReference{}, errOperationTimeout
	}
	reference := artifactReference{
		ArtifactID: "art_" + rand.Text(),
		Scope:      inventory.Scope,
		CapturedAt: inventory.EvaluatedAt,
		ClusterID:  inventory.ClusterID,
		Provenance: inventory.ClusterProvenance,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == maxArtifacts || data.buffer.Len() > maxArtifactStorage-s.bytes {
		return artifactReference{}, errArtifactLimit
	}
	// A collision is vanishingly unlikely. Never replace an active handle.
	if _, exists := s.entries[reference.ArtifactID]; exists {
		return artifactReference{}, errors.New("Could not allocate a unique MCP artifact identifier")
	}
	s.entries[reference.ArtifactID] = artifactEntry{reference: reference, data: slices.Clone(data.buffer.Bytes())}
	s.order = append(s.order, reference.ArtifactID)
	s.bytes += data.buffer.Len()
	return reference, nil
}

func (s *artifactStore) hasRoom() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries) < maxArtifacts && s.bytes < maxArtifactStorage
}

func (s *artifactStore) list() artifactList {
	s.mu.RLock()
	defer s.mu.RUnlock()
	view := artifactList{Artifacts: make([]artifactReference, 0, len(s.entries))}
	for _, id := range s.order {
		entry := s.entries[id]
		if s.policy.permitsArtifact(entry.reference.Scope, entry.reference.ClusterID, entry.reference.Provenance) {
			view.Artifacts = append(view.Artifacts, entry.reference)
		}
	}
	return view
}

func (s *artifactStore) permits(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, found := s.entries[id]
	return found && s.policy.permitsArtifact(entry.reference.Scope, entry.reference.ClusterID, entry.reference.Provenance)
}

func (s *artifactStore) load(ctx context.Context, id string) (*bundle.Bundle, error) {
	s.mu.RLock()
	entry, found := s.entries[id]
	s.mu.RUnlock()
	if !found || !s.policy.permitsArtifact(entry.reference.Scope, entry.reference.ClusterID, entry.reference.Provenance) {
		return nil, errArtifactMissing
	}
	evidence, err := decodeArtifact(ctx, bytes.NewReader(entry.data))
	if err != nil {
		return nil, err
	}
	inventory := evidence.Inventory()
	if !s.policy.permitsArtifact(inventory.Scope, inventory.ClusterID, inventory.ClusterProvenance) {
		return nil, errArtifactMissing
	}
	return evidence, nil
}

func (s *artifactStore) importStartup(ctx context.Context, directory string, names []string) error {
	if directory == "" && len(names) == 0 {
		return nil
	}
	if directory == "" || len(names) == 0 {
		return errors.New("MCP startup imports require --artifact-dir and at least one --allow-bundle")
	}
	if len(names) > maxArtifacts {
		return errArtifactLimit
	}
	seen := make([]string, 0, len(names))
	for _, name := range names {
		if !validArtifactName(name) || slices.Contains(seen, name) {
			return errors.New("MCP --allow-bundle requires a unique plain file basename")
		}
		seen = append(seen, name)
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		return errors.New("Could not open the MCP artifact directory")
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return errors.New("Could not open the MCP artifact directory")
	}
	defer dir.Close()
	for _, name := range names {
		if ctx.Err() != nil {
			return errOperationTimeout
		}
		if err := s.importFile(ctx, dir, name); err != nil {
			return err
		}
	}
	return nil
}

func (s *artifactStore) importFile(ctx context.Context, dir *os.File, name string) error {
	file, err := openArtifactFile(dir, name)
	if err != nil {
		return errors.New("MCP --allow-bundle requires a readable regular file inside the artifact directory")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("MCP --allow-bundle requires a readable regular file inside the artifact directory")
	}
	if info.Size() > maxArtifactBytes {
		return errArtifactLimit
	}
	evidence, err := decodeArtifact(ctx, file)
	if err != nil {
		return err
	}
	_, err = s.register(ctx, evidence)
	return err
}

func decodeArtifact(ctx context.Context, input io.Reader) (*bundle.Bundle, error) {
	input = contextReader{ctx: ctx, reader: input}
	evidence, err := bundle.Decode(input)
	if ctx.Err() != nil {
		return nil, errOperationTimeout
	}
	if err != nil {
		return nil, errArtifactRead
	}
	return evidence, nil
}

func validArtifactName(name string) bool {
	if len(name) == 0 || len(name) > 128 || name == "." || name == ".." ||
		strings.HasSuffix(name, ".") || !filepath.IsLocal(name) {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && !strings.ContainsRune("._-", char) {
			return false
		}
	}
	return true
}

func validClusterID(id string) bool {
	if len(id) != 64 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type boundedArtifactBuffer struct {
	buffer   bytes.Buffer
	limit    int
	ctx      context.Context
	overflow bool
}

func (b *boundedArtifactBuffer) Write(data []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) > b.limit-b.buffer.Len() {
		b.overflow = true
		return 0, errArtifactLimit
	}
	return b.buffer.Write(data)
}
