// Package dockerrun runs one program in a container through the docker CLI.
//
// It is the reusable container primitive of ai-team: the Docker gate adapter
// (pkg/gaterun) and, later, the container stage executor use it, so the
// security defaults live in one place. The package knows nothing about gates or
// stages. It turns a Spec into a `docker run` argv without a shell, runs it
// under supervision, and removes the container by name when the run is
// canceled, times out or breaks.
//
// Strict defaults are not options: every container gets --rm, a read-only root
// file system, all capabilities dropped, no-new-privileges, an explicit user,
// CPU, memory and PID limits, and --network none unless the Spec explicitly
// asks for bridge. Nothing of the controller is passed implicitly: no Docker
// socket, no home directory, no environment. Each mount and environment
// variable is listed in the Spec, and Describe reports exactly that list so a
// caller can record it as evidence without secret values.
package dockerrun

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Network modes. Only "none" is a default; "bridge" is an explicit exception.
// host and container networks are not supported.
const (
	NetworkNone   = "none"
	NetworkBridge = "bridge"
)

// Pull policies passed to `docker run --pull`.
const (
	PullMissing = "missing"
	PullNever   = "never"
)

// Mount is one bind mount. Source is an absolute host path that must exist
// (--mount, unlike -v, never creates it).
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Spec describes one container run.
type Spec struct {
	// Image is name@sha256:<digest>. A reference without a digest is rejected
	// unless AllowUnpinned is set.
	Image         string
	AllowUnpinned bool
	// Pull is missing (default) or never.
	Pull string
	// Name is the container name; it is how the run is stopped and removed.
	Name string
	// Entrypoint replaces the image entrypoint, so the program is always the
	// one the configuration names. Args follow the image.
	Entrypoint string
	Args       []string
	// Workdir is an absolute directory inside the container.
	Workdir string
	// User is uid[:gid] or a name; required (no implicit root).
	User   string
	Mounts []Mount
	// TmpfsMB is the size of the in-memory, noexec scratch /tmp. 0 means none.
	TmpfsMB int
	Network string
	// CPUs is a decimal such as "1" or "0.5".
	CPUs     string
	MemoryMB int
	// PidsLimit bounds processes in the container.
	PidsLimit int
	// Env are NAME=value entries. Values never appear in the docker argv: the
	// argv carries only `-e NAME` and the value travels in the docker CLI's own
	// environment.
	Env    []string
	Labels map[string]string
}

var (
	imagePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]{0,254}(@sha256:[0-9a-f]{64})?$`)
	namePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	userPattern    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63})?$`)
	cpusPattern    = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,2})?$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	labelPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
)

// Hard caps on the resources a Spec may request.
const (
	MaxCPUs      = 64
	MaxMemoryMB  = 64 * 1024
	MaxPidsLimit = 16384
	MaxTmpfsMB   = 4096
)

// reservedTargets are container paths no mount may cover.
var reservedTargets = []string{"/proc", "/sys", "/dev"}

// IsPinned reports whether image carries a sha256 digest.
func IsPinned(image string) bool { return strings.Contains(image, "@sha256:") }

// ClientEnvName reports whether a variable name belongs to the docker CLI's own
// environment (how it finds the daemon, its config and credential helpers).
// Such names cannot be container variables: their value would steer the
// controller's docker client instead of the program.
func ClientEnvName(name string) bool {
	// Compared without case: Windows variable names are case-insensitive
	// (docker_host is DOCKER_HOST there), and rejecting more is harmless.
	name = strings.ToUpper(name)
	switch name {
	case "PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "SSH_AUTH_SOCK":
		return true
	}
	return strings.HasPrefix(name, "DOCKER_") || strings.HasPrefix(name, "XDG_") ||
		strings.HasPrefix(name, "BUILDKIT_") || strings.HasPrefix(name, "COMPOSE_")
}

// Validate checks every field. Errors name the field and never echo an
// environment value.
func (s Spec) Validate() error {
	if !imagePattern.MatchString(s.Image) {
		return fmt.Errorf("docker: image %q is not a valid lowercase image reference", s.Image)
	}
	if !IsPinned(s.Image) && !s.AllowUnpinned {
		return fmt.Errorf("docker: image %q is not pinned by digest; use name@sha256:<64 hex> (or allow_unpinned_image explicitly)", s.Image)
	}
	switch s.Pull {
	case "", PullMissing, PullNever:
	default:
		return fmt.Errorf("docker: pull must be %s or %s", PullMissing, PullNever)
	}
	if !namePattern.MatchString(s.Name) {
		return fmt.Errorf("docker: container name %q is invalid", s.Name)
	}
	if s.Entrypoint == "" || strings.ContainsRune(s.Entrypoint, 0) {
		return fmt.Errorf("docker: entrypoint (the program) is required")
	}
	for i, arg := range s.Args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("docker: argument %d contains a NUL byte", i)
		}
	}
	if err := absContainerPath(s.Workdir); err != nil {
		return fmt.Errorf("docker: workdir %q: %w", s.Workdir, err)
	}
	if !userPattern.MatchString(s.User) {
		return fmt.Errorf("docker: user %q must be uid[:gid] or a name", s.User)
	}
	switch s.Network {
	case NetworkNone, NetworkBridge:
	default:
		return fmt.Errorf("docker: network must be %s or %s", NetworkNone, NetworkBridge)
	}
	if !cpusPattern.MatchString(s.CPUs) {
		return fmt.Errorf("docker: cpus %q must be a decimal such as 1 or 0.5", s.CPUs)
	}
	if cpus, _ := strconv.ParseFloat(s.CPUs, 64); cpus <= 0 || cpus > MaxCPUs {
		return fmt.Errorf("docker: cpus must be in (0, %d]", MaxCPUs)
	}
	if s.MemoryMB < 6 || s.MemoryMB > MaxMemoryMB {
		return fmt.Errorf("docker: memory_mb must be between 6 and %d", MaxMemoryMB)
	}
	if s.PidsLimit < 1 || s.PidsLimit > MaxPidsLimit {
		return fmt.Errorf("docker: pids_limit must be between 1 and %d", MaxPidsLimit)
	}
	if s.TmpfsMB < 0 || s.TmpfsMB > MaxTmpfsMB {
		return fmt.Errorf("docker: tmpfs_mb must be between 0 and %d", MaxTmpfsMB)
	}
	targets := map[string]bool{}
	for i, m := range s.Mounts {
		if err := absHostPath(m.Source); err != nil {
			return fmt.Errorf("docker: mount %d source: %w", i, err)
		}
		if err := absContainerPath(m.Target); err != nil {
			return fmt.Errorf("docker: mount %d target %q: %w", i, m.Target, err)
		}
		if m.Target == "/" {
			return fmt.Errorf("docker: mount %d must not cover the container root", i)
		}
		for _, reserved := range reservedTargets {
			if m.Target == reserved || strings.HasPrefix(m.Target, reserved+"/") {
				return fmt.Errorf("docker: mount %d target %q is reserved", i, m.Target)
			}
		}
		if targets[m.Target] {
			return fmt.Errorf("docker: mount target %q is used twice", m.Target)
		}
		targets[m.Target] = true
	}
	if s.TmpfsMB > 0 && targets["/tmp"] {
		return fmt.Errorf("docker: /tmp is both a mount and the tmpfs scratch")
	}
	seen := map[string]bool{}
	for _, entry := range s.Env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !envNamePattern.MatchString(name) {
			return fmt.Errorf("docker: environment entry must be NAME=value with a valid name")
		}
		if ClientEnvName(name) {
			return fmt.Errorf("docker: variable name %q is reserved for the docker client and cannot be passed to the container", name)
		}
		if seen[name] {
			return fmt.Errorf("docker: variable %q is set twice", name)
		}
		seen[name] = true
	}
	for key, value := range s.Labels {
		if !labelPattern.MatchString(key) || strings.ContainsAny(value, "\x00\n\r") {
			return fmt.Errorf("docker: label %q is invalid", key)
		}
	}
	return nil
}

func absHostPath(p string) error {
	if !strings.HasPrefix(p, "/") && !(len(p) > 2 && p[1] == ':') {
		return fmt.Errorf("must be absolute")
	}
	if strings.ContainsAny(p, ",\"\n\r\x00") {
		return fmt.Errorf("contains a character that cannot be passed safely to --mount")
	}
	return nil
}

func absContainerPath(p string) error {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return fmt.Errorf("must be an absolute clean path")
	}
	if strings.ContainsAny(p, ",\"\n\r\x00:") {
		return fmt.Errorf("contains a character that cannot be passed safely to --mount")
	}
	return nil
}

// RunArgs returns the argv of `docker run` without the leading "docker". It
// contains no environment value.
func (s Spec) RunArgs() ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	args := []string{
		"run", "--rm", "--name", s.Name, "--pull", orDefault(s.Pull, PullMissing), "-i",
		"--network", s.Network,
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", s.User,
		"--cpus", s.CPUs,
		"--memory", fmt.Sprintf("%dm", s.MemoryMB),
		"--memory-swap", fmt.Sprintf("%dm", s.MemoryMB),
		"--pids-limit", strconv.Itoa(s.PidsLimit),
		"--workdir", s.Workdir,
	}
	if s.TmpfsMB > 0 {
		args = append(args, "--tmpfs", fmt.Sprintf("/tmp:rw,noexec,nosuid,nodev,size=%dm", s.TmpfsMB))
	}
	keys := make([]string, 0, len(s.Labels))
	for key := range s.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+s.Labels[key])
	}
	for _, m := range s.Mounts {
		value := "type=bind,source=" + m.Source + ",target=" + m.Target
		if m.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	for _, entry := range s.Env {
		name, _, _ := strings.Cut(entry, "=")
		args = append(args, "-e", name)
	}
	args = append(args, "--entrypoint", s.Entrypoint, s.Image)
	return append(args, s.Args...), nil
}

// Describe is the evidence record of the run: what the container was allowed
// to see and do. It lists names, never values.
func (s Spec) Describe() map[string]any {
	mounts := make([]map[string]any, len(s.Mounts))
	for i, m := range s.Mounts {
		mounts[i] = map[string]any{"source": m.Source, "target": m.Target, "read_only": m.ReadOnly}
	}
	names := make([]string, 0, len(s.Env))
	for _, entry := range s.Env {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	return map[string]any{
		"image":          s.Image,
		"image_pinned":   IsPinned(s.Image),
		"container_name": s.Name,
		"network":        s.Network,
		"user":           s.User,
		"workdir":        s.Workdir,
		"mounts":         mounts,
		"tmpfs_mb":       s.TmpfsMB,
		"env_names":      names,
		"cpus":           s.CPUs,
		"memory_mb":      s.MemoryMB,
		"pids_limit":     s.PidsLimit,
		"read_only_root": true,
		"cap_drop":       "ALL",
		"no_new_privs":   true,
		"docker_socket":  false,
		"host_home":      false,
		"pull":           orDefault(s.Pull, PullMissing),
	}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
