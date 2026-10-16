package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

// Provenance of the active template.
//
// The web replaces .ai-team/config.yaml (TemplateStore.Publish), so "the active
// template" cannot be the anchor of trust by itself: a version published through
// the web before the trusted-config rule, with a command gate, stays active and
// would trust itself. The anchor lives outside the template content, under
// .ai-team/templates, and only Publish writes it:
//
//   - web-written/<sha256> is an append-only registry of the hashes of every
//     byte sequence the web wrote into config.yaml;
//   - trusted-baseline.yaml is the last file configuration that was NOT
//     web-written (the human-authored file Publish replaced).
//
// The active file is "web-written" while its bytes still hash into the registry.
// A person who edits the file changes the hash and so takes responsibility for
// it: the file is trusted code again. Until then the file is compared with the
// baseline like any other stored template, so commands and infrastructure a web
// publication carried are not trusted. A person who restores exactly the bytes
// of an earlier web publication (for example by git checkout) gets the same
// treatment; any edit, even a comment, lifts it.
//
// Limitation: a file the web wrote before this registry existed has no record
// and is treated as a file configuration. Review it with `ai-team template
// audit`.

const (
	webWrittenDirName = "web-written"
	baselineFileName  = "trusted-baseline.yaml"
)

func (s *TemplateStore) webWrittenPath(data []byte) string {
	return filepath.Join(s.target, ".ai-team", "templates", webWrittenDirName, TemplateVersionID(data))
}

func (s *TemplateStore) baselinePath() string {
	return filepath.Join(s.target, ".ai-team", "templates", baselineFileName)
}

// markWebWritten records that the web is about to write data into config.yaml.
func (s *TemplateStore) markWebWritten(data []byte) error {
	path := s.webWrittenPath(data)
	if err := safeio.WriteRegularFileNoFollow(path, []byte(TemplateVersionID(data)+"\n"), 0444); err != nil {
		if _, readErr := safeio.ReadRegularFile(path, 128); readErr == nil {
			return nil
		}
		return fmt.Errorf("записать web-written: %w", err)
	}
	return nil
}

// isWebWritten reports whether data is a byte sequence the web wrote.
func (s *TemplateStore) isWebWritten(data []byte) (bool, error) {
	if _, err := safeio.ExistingDir(s.target, ".ai-team", "templates", webWrittenDirName); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("реестр web-written: %w", err)
	}
	if _, err := safeio.ReadRegularFile(s.webWrittenPath(data), 128); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("реестр web-written: %w", err)
	}
	return true, nil
}

// saveBaseline keeps a file configuration the web is about to replace.
func (s *TemplateStore) saveBaseline(data []byte) error {
	return atomicReplaceRegular(s.baselinePath(), data, 0644)
}

// trustAnchor is what a stored template is compared with.
type trustAnchor struct {
	// active is the current config.yaml; hasActive is false when there is none.
	active    []byte
	hasActive bool
	// webWritten is true while active is bytes the web wrote.
	webWritten bool
	// baseline is the trusted file configuration: the active file itself when it
	// is not web-written, else the last non-web file saved by Publish, else nil
	// (nothing trusted: every command and infrastructure section is foreign).
	baseline *Config
}

func (s *TemplateStore) anchor() (trustAnchor, error) {
	active, _, err := s.ReadCurrent()
	if errors.Is(err, os.ErrNotExist) {
		return trustAnchor{}, nil
	}
	if err != nil {
		return trustAnchor{}, fmt.Errorf("прочитать активный шаблон: %w", err)
	}
	anchor := trustAnchor{active: active, hasActive: true}
	if anchor.webWritten, err = s.isWebWritten(active); err != nil {
		return trustAnchor{}, err
	}
	source := active
	if anchor.webWritten {
		source, err = safeio.ReadRegularFile(s.baselinePath(), MaxTemplateYAMLBytes)
		if errors.Is(err, os.ErrNotExist) {
			return anchor, nil
		}
		if err != nil {
			return trustAnchor{}, fmt.Errorf("базовый шаблон: %w", err)
		}
	}
	if anchor.baseline, err = ParseYAML(source); err != nil {
		return trustAnchor{}, fmt.Errorf("доверенный шаблон: %w", err)
	}
	return anchor, nil
}
