package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// prepareControllerBriefRoot moves a legacy run-local brief directory into
// controller-owned state before the worker starts. The run evidence tree is
// never created here: for a fresh run, only state/briefs/<runID> is created.
func prepareControllerBriefRoot(targetDir, runID string) (string, error) {
	if err := evidence.ValidateRunID(runID); err != nil {
		return "", err
	}
	stateRoot, err := safeio.EnsureDir(targetDir, ".ai-team", "state", "briefs")
	if err != nil {
		return "", err
	}
	if err := recoverLegacyBriefQuarantine(targetDir, runID); err != nil {
		return "", fmt.Errorf("recover interrupted legacy business brief cleanup: %w", err)
	}
	canonical := filepath.Join(stateRoot, runID)
	legacyPath, legacyExists, err := existingLegacyBrief(targetDir, runID)
	if err != nil {
		return "", err
	}
	if legacyExists {
		legacyTree, err := readBriefTree(legacyPath)
		if err != nil {
			return "", fmt.Errorf("validate legacy business brief: %w", err)
		}
		canonicalInfo, canonicalErr := os.Lstat(canonical)
		switch {
		case errors.Is(canonicalErr, os.ErrNotExist):
			if len(legacyTree) == 0 {
				if _, err := safeio.EnsureDir(stateRoot, runID); err != nil {
					return "", fmt.Errorf("create controller brief root for empty legacy cleanup: %w", err)
				}
				if err := removeLegacyBrief(targetDir, runID, legacyTree); err != nil {
					return "", err
				}
			} else if err := secureMigrateLegacyBrief(targetDir, runID, legacyTree); err != nil {
				return "", fmt.Errorf("migrate legacy business brief: %w", err)
			}
		case canonicalErr != nil:
			return "", canonicalErr
		case canonicalInfo.Mode()&os.ModeSymlink != 0 || !canonicalInfo.IsDir():
			return "", fmt.Errorf("controller business brief path %q must be a real directory", canonical)
		default:
			canonicalTree, err := readBriefTree(canonical)
			if err != nil {
				return "", fmt.Errorf("validate controller business brief: %w", err)
			}
			switch {
			case len(legacyTree) == 0:
				if err := removeLegacyBrief(targetDir, runID, legacyTree); err != nil {
					return "", err
				}
			case len(canonicalTree) == 0:
				if err := secureRemoveEmptyCanonicalBrief(targetDir, runID); err != nil {
					return "", fmt.Errorf("replace empty brief mountpoint: %w", err)
				}
				if err := secureMigrateLegacyBrief(targetDir, runID, legacyTree); err != nil {
					return "", fmt.Errorf("migrate legacy business brief: %w", err)
				}
			case equalBriefTrees(legacyTree, canonicalTree):
				if err := removeLegacyBrief(targetDir, runID, legacyTree); err != nil {
					return "", err
				}
			case briefTreeIsSubset(legacyTree, canonicalTree):
				// An interrupted legacy cleanup may already have removed some
				// duplicate files. Every remaining legacy byte is still present
				// in the controller-owned canonical tree, so cleanup is lossless.
				if err := removeLegacyBrief(targetDir, runID, legacyTree); err != nil {
					return "", err
				}
			default:
				return "", fmt.Errorf("legacy and controller business brief stores conflict for run %q", runID)
			}
		}
	}
	// A prior cleanup can crash after removing runs/<runID>/brief but before
	// removing the now-empty run root. Retry this idempotent removal even when
	// no legacy brief leaf remains; non-empty run roots are preserved.
	if err := secureRemoveEmptyLegacyRunRoot(targetDir, runID); err != nil {
		return "", err
	}
	return safeio.EnsureDir(stateRoot, runID)
}

func existingLegacyBrief(targetDir, runID string) (briefPath string, exists bool, err error) {
	runRoot, err := safeio.ExistingDir(targetDir, ".ai-team", "runs", runID)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect legacy run directory: %w", err)
	}
	briefPath = filepath.Join(runRoot, "brief")
	info, err := os.Lstat(briefPath)
	if errors.Is(err, os.ErrNotExist) {
		return briefPath, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", false, fmt.Errorf("legacy business brief path %q must be a real directory", briefPath)
	}
	return briefPath, true, nil
}

// readBriefTree accepts only the flat immutable data layout produced by
// FileBriefStore. It validates sidecar identities before any legacy bytes are
// moved, so migration cannot bless conflicting IDs or hashes.
func readBriefTree(root string) (map[string][]byte, error) {
	if err := safeio.ValidateTree(root); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	tree := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("business brief directory %q contains nested directory %q", root, entry.Name())
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("business brief file %q must be regular and have no symlink", path)
		}
		if err := requireSingleLinkBriefFile(path, info); err != nil {
			return nil, err
		}
		limit := int64(maxBriefBytes)
		if strings.HasSuffix(entry.Name(), ".json") {
			limit = 1 << 20
		} else if !strings.HasSuffix(entry.Name(), ".md") {
			return nil, fmt.Errorf("business brief contains unsupported file %q", entry.Name())
		}
		data, err := safeio.ReadRegularFile(path, limit)
		if err != nil {
			return nil, err
		}
		tree[entry.Name()] = data
	}
	if err := validateBriefTreeData(root, tree); err != nil {
		return nil, err
	}
	return tree, nil
}

func validateBriefTreeData(root string, tree map[string][]byte) error {
	markdown := make(map[string][]byte)
	for name, content := range tree {
		if strings.HasSuffix(name, ".md") {
			if len(content) == 0 {
				return fmt.Errorf("business brief version %q is empty", name)
			}
			markdown[name] = content
		} else if !strings.HasSuffix(name, ".json") {
			return fmt.Errorf("business brief contains unsupported file %q", name)
		}
	}
	for name, content := range markdown {
		metadataName := strings.TrimSuffix(name, filepath.Ext(name)) + ".json"
		metadataBytes, exists := tree[metadataName]
		if !exists {
			continue // Older stores may not yet have generated this sidecar.
		}
		expected := briefMetadata(name, content)
		var got struct {
			ID     string `json:"id"`
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		}
		decoder := json.NewDecoder(bytes.NewReader(metadataBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&got); err != nil {
			return fmt.Errorf("decode business brief metadata %q: %w", metadataName, err)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return fmt.Errorf("business brief metadata %q has trailing data", metadataName)
		}
		if got.ID != expected.ID || got.Path != expected.Path || got.SHA256 != expected.SHA256 {
			return fmt.Errorf("business brief metadata %q conflicts with its content", metadataName)
		}
	}
	for name := range tree {
		if strings.HasSuffix(name, ".json") {
			markdownName := strings.TrimSuffix(name, filepath.Ext(name)) + ".md"
			if _, exists := markdown[markdownName]; !exists {
				return fmt.Errorf("business brief metadata %q has no matching version", name)
			}
		}
	}
	return nil
}

func briefMetadata(name string, content []byte) struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
} {
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	return struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}{ID: "brief-" + hash[:16], Path: filepath.ToSlash(filepath.Join("brief", name)), SHA256: hash}
}

func equalBriefTrees(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for name, data := range left {
		if !bytes.Equal(data, right[name]) {
			return false
		}
	}
	return true
}

func briefTreeIsSubset(subset, superset map[string][]byte) bool {
	if len(subset) > len(superset) {
		return false
	}
	for name, data := range subset {
		other, ok := superset[name]
		if !ok || !bytes.Equal(data, other) {
			return false
		}
	}
	return true
}

func removeLegacyBrief(targetDir, runID string, expected map[string][]byte) error {
	if err := secureRemoveLegacyBrief(targetDir, runID, expected); err != nil {
		return fmt.Errorf("remove migrated legacy brief: %w", err)
	}
	return nil
}
