package change

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	changeApprovalTTL     = 30 * time.Minute
	maximumPendingChanges = 64
)

type pendingChange struct {
	ID        string
	Workspace opsWorkspace
	CreatedAt time.Time
	ExpiresAt time.Time
	Original  map[string]string
	Updated   map[string]string
	Created   map[string]bool
}

type changeApproval struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
}

type stagedChange struct {
	path      string
	target    string
	temporary string
	backup    string
	created   bool
}

func newPendingChange(workspace opsWorkspace, result changeSimulationResult, now time.Time) (pendingChange, error) {
	if len(result.ChangedFiles) == 0 || len(result.updatedFiles) == 0 {
		return pendingChange{}, fmt.Errorf("change proposal has no applicable files")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return pendingChange{}, fmt.Errorf("generate approval ID: %w", err)
	}
	return pendingChange{
		ID: hex.EncodeToString(random), Workspace: workspace,
		CreatedAt: now, ExpiresAt: now.Add(changeApprovalTTL),
		Original: cloneChangeContents(result.originalFiles), Updated: cloneChangeContents(result.updatedFiles),
		Created: cloneChangeFlags(result.createdFiles),
	}, nil
}

func cloneChangeFlags(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for path, value := range source {
		result[path] = value
	}
	return result
}

func cloneChangeContents(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for path, content := range source {
		result[path] = content
	}
	return result
}

func applyPendingChange(change pendingChange) ([]string, error) {
	paths := make([]string, 0, len(change.Updated))
	for path := range change.Updated {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	targets := make(map[string]string, len(paths))
	modes := make(map[string]os.FileMode, len(paths))
	for _, path := range paths {
		if change.Created[path] {
			if err := validateChangeSimulationCreatePath(change.Workspace.Root, path); err != nil {
				return nil, fmt.Errorf("stale preview: cannot create %q: %w", path, err)
			}
			targets[path] = filepath.Join(change.Workspace.Root, filepath.FromSlash(path))
			modes[path] = 0o644
			continue
		}
		current, normalized, err := loadChangeSimulationFile(change.Workspace.Root, path)
		if err != nil {
			return nil, fmt.Errorf("verify %q: %w", path, err)
		}
		if normalized != path || current != change.Original[path] {
			return nil, fmt.Errorf("stale preview: %q changed after proposal generation", path)
		}
		target := filepath.Join(change.Workspace.Root, filepath.FromSlash(path))
		state, err := os.Stat(target)
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", path, err)
		}
		targets[path] = target
		modes[path] = state.Mode().Perm()
	}

	staged := make([]stagedChange, 0, len(paths))
	cleanup := func() {
		for _, item := range staged {
			_ = os.Remove(item.temporary)
			_ = os.Remove(item.backup)
		}
	}
	defer cleanup()
	for _, path := range paths {
		target := targets[path]
		temporary, err := os.CreateTemp(filepath.Dir(target), ".akritas-apply-*")
		if err != nil {
			return nil, fmt.Errorf("stage %q: %w", path, err)
		}
		item := stagedChange{
			path: path, target: target, temporary: temporary.Name(),
			backup: temporary.Name() + ".bak", created: change.Created[path],
		}
		staged = append(staged, item)
		if err := temporary.Chmod(modes[path]); err != nil {
			_ = temporary.Close()
			return nil, fmt.Errorf("preserve mode for %q: %w", path, err)
		}
		if _, err := temporary.WriteString(change.Updated[path]); err != nil {
			_ = temporary.Close()
			return nil, fmt.Errorf("stage content for %q: %w", path, err)
		}
		if err := temporary.Sync(); err != nil {
			_ = temporary.Close()
			return nil, fmt.Errorf("sync staged %q: %w", path, err)
		}
		if err := temporary.Close(); err != nil {
			return nil, fmt.Errorf("close staged %q: %w", path, err)
		}
	}

	applied := 0
	for index, item := range staged {
		if item.created {
			if err := os.Link(item.temporary, item.target); err != nil {
				rollbackAppliedChanges(staged[:applied])
				return nil, fmt.Errorf("create %q: %w", item.path, err)
			}
			if err := os.Remove(item.temporary); err != nil {
				_ = os.Remove(item.target)
				rollbackAppliedChanges(staged[:applied])
				return nil, fmt.Errorf("finalize created %q: %w", item.path, err)
			}
			staged[index].temporary = ""
			applied++
			continue
		}
		if err := os.Rename(item.target, item.backup); err != nil {
			rollbackAppliedChanges(staged[:applied])
			return nil, fmt.Errorf("backup %q: %w", item.path, err)
		}
		if err := os.Rename(item.temporary, item.target); err != nil {
			_ = os.Rename(item.backup, item.target)
			rollbackAppliedChanges(staged[:applied])
			return nil, fmt.Errorf("apply %q: %w", item.path, err)
		}
		staged[index].temporary = ""
		applied++
	}
	for index := range staged {
		_ = os.Remove(staged[index].backup)
		staged[index].backup = ""
	}
	return paths, nil
}

func rollbackAppliedChanges(items []stagedChange) {
	for index := len(items) - 1; index >= 0; index-- {
		_ = os.Remove(items[index].target)
		if !items[index].created {
			_ = os.Rename(items[index].backup, items[index].target)
		}
	}
}
