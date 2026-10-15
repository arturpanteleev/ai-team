// Package gatelib is the versioned library of named gate definitions.
//
// The library lives in Go code and changes only through a PR. The UI, REST
// API and MCP read the catalog but cannot edit or upload definitions. A
// definition is immutable once published: a change of behavior or parameters
// is a new version, and old versions stay resolvable forever, so a published
// template or a started task that pinned "id@version" keeps the exact
// definition it was validated against.
package gatelib

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Registry is a set of immutable gate definitions keyed by ID and version.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]map[int]workflow.GateDefinition
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{defs: map[string]map[int]workflow.GateDefinition{}}
}

// Register adds a definition. Re-registering an existing ID+version is an
// error: published definitions are immutable. The definition is validated
// (ID, version, kind, parameter schema) before it is stored.
func (r *Registry) Register(def workflow.GateDefinition) error {
	if err := validateDefinition(def); err != nil {
		return fmt.Errorf("gate library: %s: %w", def.Ref(), err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.defs[def.ID][def.Version]; exists {
		return fmt.Errorf("gate library: %s уже опубликован; опубликованные определения неизменяемы, выпустите новую версию", def.Ref())
	}
	if r.defs[def.ID] == nil {
		r.defs[def.ID] = map[int]workflow.GateDefinition{}
	}
	r.defs[def.ID][def.Version] = cloneDefinition(def)
	return nil
}

// MustRegister is Register for package initialization of the built-in
// library; a malformed built-in definition is a programming error.
func (r *Registry) MustRegister(def workflow.GateDefinition) {
	if err := r.Register(def); err != nil {
		panic(err)
	}
}

// Clone returns an independent copy, used to model "a new library version was
// published" without touching the process-wide library.
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := NewRegistry()
	for id, versions := range r.defs {
		out.defs[id] = map[int]workflow.GateDefinition{}
		for version, def := range versions {
			out.defs[id][version] = cloneDefinition(def)
		}
	}
	return out
}

// Lookup resolves an exact ID+version. The error names what is missing and
// lists the available versions or IDs.
func (r *Registry) Lookup(id string, version int) (workflow.GateDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions, ok := r.defs[id]
	if !ok {
		return workflow.GateDefinition{}, fmt.Errorf("неизвестный гейт %q (доступны: %s)", id, strings.Join(r.idsLocked(), ", "))
	}
	def, ok := versions[version]
	if !ok {
		return workflow.GateDefinition{}, fmt.Errorf("у гейта %q нет версии %d (доступны: %s)", id, version, joinInts(sortedVersions(versions)))
	}
	return cloneDefinition(def), nil
}

// Latest returns the highest published version of id. It is for catalogs and
// authoring hints only; templates must always pin an exact version.
func (r *Registry) Latest(id string) (workflow.GateDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	versions := sortedVersions(r.defs[id])
	if len(versions) == 0 {
		return workflow.GateDefinition{}, false
	}
	return cloneDefinition(r.defs[id][versions[len(versions)-1]]), true
}

// Catalog lists every definition (all versions) sorted by ID then version.
func (r *Registry) Catalog() []workflow.GateDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []workflow.GateDefinition
	for _, id := range r.idsLocked() {
		for _, version := range sortedVersions(r.defs[id]) {
			out = append(out, cloneDefinition(r.defs[id][version]))
		}
	}
	return out
}

func (r *Registry) idsLocked() []string {
	ids := make([]string, 0, len(r.defs))
	for id := range r.defs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Digest is the SHA-256 of the definition's canonical JSON.
func Digest(def workflow.GateDefinition) string {
	data, _ := json.Marshal(def) // GateDefinition has only JSON-safe fields
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Pin returns the pin of a resolved definition.
func Pin(def workflow.GateDefinition) workflow.GatePin {
	return workflow.GatePin{Gate: def.ID, Version: def.Version, Kind: def.Kind, Digest: Digest(def)}
}

func sortedVersions(versions map[int]workflow.GateDefinition) []int {
	out := make([]int, 0, len(versions))
	for version := range versions {
		out = append(out, version)
	}
	sort.Ints(out)
	return out
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprint(value)
	}
	return strings.Join(parts, ", ")
}

func cloneDefinition(def workflow.GateDefinition) workflow.GateDefinition {
	def.ResultTypes = append([]string(nil), def.ResultTypes...)
	def.LinkKinds = append([]string(nil), def.LinkKinds...)
	params := make([]workflow.GateParam, len(def.Params))
	for i, param := range def.Params {
		param.Enum = append([]string(nil), param.Enum...)
		if param.Min != nil {
			bound := *param.Min
			param.Min = &bound
		}
		params[i] = param
	}
	if len(params) == 0 {
		params = nil
	}
	def.Params = params
	return def
}

func validateDefinition(def workflow.GateDefinition) error {
	if !workflow.ValidGateID(def.ID) {
		return fmt.Errorf("ID %q невалиден", def.ID)
	}
	if def.Version <= 0 {
		return fmt.Errorf("версия должна быть положительной")
	}
	if !def.Kind.Valid() {
		return fmt.Errorf("вид %q неизвестен", def.Kind)
	}
	if strings.TrimSpace(def.Title) == "" {
		return fmt.Errorf("title обязателен")
	}
	seen := map[string]bool{}
	for _, param := range def.Params {
		if param.Name == "" || seen[param.Name] {
			return fmt.Errorf("параметр с пустым или повторяющимся именем %q", param.Name)
		}
		seen[param.Name] = true
		switch param.Type {
		case workflow.GateParamString, workflow.GateParamInt, workflow.GateParamBool, workflow.GateParamStringList:
		case workflow.GateParamEnum:
			if len(param.Enum) == 0 {
				return fmt.Errorf("параметр %q типа enum требует список значений", param.Name)
			}
		default:
			return fmt.Errorf("параметр %q: тип %q неизвестен", param.Name, param.Type)
		}
	}
	return nil
}
