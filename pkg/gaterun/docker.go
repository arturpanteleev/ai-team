package gaterun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/dockerrun"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
)

// Container paths of the Docker gate. The program is told these paths in the
// input document (paths.repo_root, paths.evidence_dir, artifact.path).
const (
	ContainerRepoRoot    = "/gate/repo"
	ContainerEvidenceDir = "/gate/evidence"
	ContainerArtifact    = "/gate/artifact"
)

// Defaults of a docker_program gate.
const (
	DefaultDockerCPUs      = "1"
	DefaultDockerMemoryMB  = 512
	DefaultDockerPidsLimit = 128
	DefaultDockerTmpfsMB   = 64
	// nobodyUser is used when the controller itself runs as root: a container
	// never inherits root implicitly.
	nobodyUser = "65534:65534"
)

// GateAttemptLabel marks every gate container with its attempt ID.
const GateAttemptLabel = "ai-team.gate-attempt"

// OrphanAge is how old a gate container must be before the start-up sweep
// removes it: longer than the longest allowed gate run, so a container of
// another live controller on the same daemon is never touched.
const OrphanAge = MaxTimeout + 15*time.Minute

// DockerMount is one explicit extra mount of a repository path.
type DockerMount struct {
	// Path is repository-relative and slash-separated.
	Path string
	// Target is the absolute container path.
	Target string
	// Writable is false (read-only) unless the configuration says rw.
	Writable bool
}

// DockerSettings is the container half of a docker_program gate. Zero values
// mean the strict defaults; every field that widens access is an explicit,
// recorded exception.
type DockerSettings struct {
	// Image must be name@sha256:<digest>.
	Image         string
	AllowUnpinned bool
	// Pull is "" (docker default, missing) or "never".
	Pull string
	// Network is "" or none (default) or bridge.
	Network  string
	CPUs     string
	MemoryMB int
	// PidsLimit and TmpfsMB use the defaults when 0; TmpfsMB -1 disables the
	// scratch /tmp.
	PidsLimit int
	TmpfsMB   int
	// User is uid[:gid] or a name; empty means the controller's uid:gid (or
	// nobody when the controller is root).
	User string
	// RepoWritable mounts the repository read-write; default read-only.
	RepoWritable bool
	Mounts       []DockerMount
}

func (d DockerSettings) network() string {
	if d.Network == "" {
		return dockerrun.NetworkNone
	}
	return d.Network
}

func (d DockerSettings) cpus() string {
	if d.CPUs == "" {
		return DefaultDockerCPUs
	}
	return d.CPUs
}

func (d DockerSettings) memoryMB() int {
	if d.MemoryMB == 0 {
		return DefaultDockerMemoryMB
	}
	return d.MemoryMB
}

func (d DockerSettings) pidsLimit() int {
	if d.PidsLimit == 0 {
		return DefaultDockerPidsLimit
	}
	return d.PidsLimit
}

func (d DockerSettings) tmpfsMB() int {
	switch {
	case d.TmpfsMB == 0:
		return DefaultDockerTmpfsMB
	case d.TmpfsMB < 0:
		return 0
	}
	return d.TmpfsMB
}

// Exceptions lists, in words, everything in the settings that is wider than
// the strict defaults. It is stored in invocation.json. It names paths and
// variable names, never values.
func (d DockerSettings) Exceptions(envNames []string) []string {
	var out []string
	if d.network() != dockerrun.NetworkNone {
		out = append(out, "network="+d.network())
	}
	if d.RepoWritable {
		out = append(out, "repository mounted read-write (.git stays read-only)")
	}
	for _, m := range d.Mounts {
		mode := "read-only"
		if m.Writable {
			mode = "read-write"
		}
		out = append(out, fmt.Sprintf("extra mount %s -> %s (%s)", m.Path, m.Target, mode))
	}
	if len(envNames) > 0 {
		out = append(out, "environment passed: "+strings.Join(envNames, ", "))
	}
	switch d.User {
	case "":
	case "0", "root", "0:0", "root:root":
		out = append(out, "runs as root")
	default:
		out = append(out, "explicit user "+d.User)
	}
	if d.AllowUnpinned && !dockerrun.IsPinned(d.Image) {
		out = append(out, "image is not pinned by digest")
	}
	if d.tmpfsMB() == 0 {
		out = append(out, "no scratch /tmp")
	}
	return out
}

// Validate checks the settings with the rules dockerrun enforces at launch
// time, so a bad configuration fails before any run starts.
func (d DockerSettings) Validate() error {
	probe := dockerrun.Spec{
		Image: d.Image, AllowUnpinned: d.AllowUnpinned, Pull: d.Pull, Name: "validate",
		Entrypoint: "x", Workdir: ContainerRepoRoot, User: orUser(d.User),
		Network: d.network(), CPUs: d.cpus(), MemoryMB: d.memoryMB(), PidsLimit: d.pidsLimit(), TmpfsMB: d.tmpfsMB(),
	}
	targets := map[string]bool{ContainerRepoRoot: true, ContainerEvidenceDir: true, ContainerArtifact: true}
	for _, m := range d.Mounts {
		if err := validMountPath(m.Path); err != nil {
			return fmt.Errorf("docker gate: mount path %q: %w", m.Path, err)
		}
		if targets[m.Target] {
			return fmt.Errorf("docker gate: mount target %q is reserved or repeated", m.Target)
		}
		targets[m.Target] = true
		probe.Mounts = append(probe.Mounts, dockerrun.Mount{Source: "/probe/" + m.Path, Target: m.Target, ReadOnly: !m.Writable})
		if m.Writable && hasGitElement(m.Path) {
			return fmt.Errorf("docker gate: mount %q: a gate must not get write access to .git", m.Path)
		}
		if strings.HasPrefix(m.Target, ContainerEvidenceDir+"/") || strings.HasPrefix(m.Target, ContainerArtifact+"/") {
			return fmt.Errorf("docker gate: mount target %q lies inside a reserved directory", m.Target)
		}
	}
	return probe.Validate()
}

func orUser(user string) string {
	if user == "" {
		return "1000:1000"
	}
	return user
}

func validMountPath(p string) error {
	if p == "." {
		return fmt.Errorf("the repository itself is mounted by repo_access; name a subpath")
	}
	return validRelDir(p)
}

var cpusParam = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,2})?$`)

// ParseDockerParams validates the parameters of a docker_program gate
// reference against the library schema and the deeper rules the schema cannot
// express. The result has Docker set; Command is the container argv.
func ParseDockerParams(params map[string]any) (Config, error) {
	def, err := gatelib.Default().Lookup(gatelib.GateDockerProgram, 1)
	if err != nil {
		return Config{}, err
	}
	if err := gatelib.ValidateParams(def, params); err != nil {
		return Config{}, err
	}
	settings := &DockerSettings{
		Image:         gatelib.StringParam(params, "image"),
		AllowUnpinned: params["allow_unpinned_image"] == true,
		Pull:          gatelib.StringParam(params, "pull"),
		Network:       gatelib.StringParam(params, "network"),
		CPUs:          gatelib.StringParam(params, "cpus"),
		MemoryMB:      gatelib.IntParam(params, "memory_mb"),
		PidsLimit:     gatelib.IntParam(params, "pids_limit"),
		User:          gatelib.StringParam(params, "user"),
		RepoWritable:  gatelib.StringParam(params, "repo_access") == "rw",
	}
	if settings.CPUs != "" && !cpusParam.MatchString(settings.CPUs) {
		return Config{}, fmt.Errorf("docker gate: cpus %q must be a decimal such as 1 or 0.5", settings.CPUs)
	}
	if _, ok := params["tmpfs_mb"]; ok {
		settings.TmpfsMB = gatelib.IntParam(params, "tmpfs_mb")
		if settings.TmpfsMB == 0 {
			settings.TmpfsMB = -1
		}
	}
	for _, spec := range gatelib.StringListParam(params, "mounts") {
		mount, err := parseMount(spec)
		if err != nil {
			return Config{}, fmt.Errorf("docker gate: mounts: %w", err)
		}
		settings.Mounts = append(settings.Mounts, mount)
	}
	cfg := Config{
		Command:        gatelib.StringListParam(params, "command"),
		Timeout:        DefaultTimeout,
		CWD:            ".",
		EnvAllow:       gatelib.StringListParam(params, "env_allow"),
		MaxOutputBytes: DefaultMaxOutputBytes,
		Docker:         settings,
	}
	if _, ok := params["timeout_seconds"]; ok {
		cfg.Timeout = time.Duration(gatelib.IntParam(params, "timeout_seconds")) * time.Second
	}
	if _, ok := params["max_output_bytes"]; ok {
		cfg.MaxOutputBytes = int64(gatelib.IntParam(params, "max_output_bytes"))
	}
	if value := gatelib.StringParam(params, "cwd"); value != "" {
		cfg.CWD = value
	}
	return cfg, cfg.Validate()
}

// parseMount reads "<repo-relative path>:<container path>[:ro|rw]".
func parseMount(spec string) (DockerMount, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return DockerMount{}, fmt.Errorf("%q must be <repository path>:<container path>[:ro|rw]", spec)
	}
	mount := DockerMount{Path: parts[0], Target: parts[1]}
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
		case "rw":
			mount.Writable = true
		default:
			return DockerMount{}, fmt.Errorf("%q: the mode must be ro or rw", spec)
		}
	}
	return mount, nil
}

// DockerLauncher runs the gate program in a container through the docker CLI
// and speaks the same protocol as the host launcher. The settings come with
// each LaunchSpec; the launcher itself is stateless and safe for concurrent
// use.
type DockerLauncher struct {
	// Docker is the container primitive; the zero value uses the docker CLI.
	Docker dockerrun.Runner
	// Getuid and Getgid default to the controller's ids; they choose the
	// container user when the settings name none.
	Getuid, Getgid func() int
}

// Kind implements Launcher.
func (DockerLauncher) Kind() workflow.GateKind { return workflow.GateKindDocker }

// Launch implements Launcher.
func (l DockerLauncher) Launch(ctx context.Context, spec LaunchSpec) Outcome {
	fail := func(err error) Outcome {
		return Outcome{ExitCode: -1, Err: err, Detail: "container not started"}
	}
	if spec.Docker == nil {
		return fail(errors.New("docker launcher: no docker settings in the launch spec"))
	}
	if len(spec.Argv) == 0 {
		return fail(errors.New("empty argv"))
	}
	settings := *spec.Docker
	repoRoot, evidenceDir := spec.Input.Paths.RepoRoot, spec.Input.Paths.EvidenceDir

	// The program sees container paths only. The input document is rebuilt
	// from the decoded input, so no host path leaks in.
	input := spec.Input
	input.Paths = PathsInfo{RepoRoot: ContainerRepoRoot, EvidenceDir: ContainerEvidenceDir}
	mounts := []dockerrun.Mount{
		{Source: repoRoot, Target: ContainerRepoRoot, ReadOnly: !settings.RepoWritable},
		{Source: evidenceDir, Target: ContainerEvidenceDir, ReadOnly: false},
	}
	if hostPath := spec.Input.Artifact.Path; hostPath != "" {
		if rel, ok := within(repoRoot, hostPath); ok {
			input.Artifact.Path = path.Join(ContainerRepoRoot, rel)
		} else {
			if err := noSymlinkFile(hostPath); err != nil {
				return fail(fmt.Errorf("artifact file: %w", err))
			}
			// The result is exposed read-only, as one file.
			target := ContainerArtifact + "/" + filepath.Base(hostPath)
			mounts = append(mounts, dockerrun.Mount{Source: hostPath, Target: target, ReadOnly: true})
			input.Artifact.Path = target
		}
	}
	if settings.RepoWritable {
		// A writable repository must not let the container plant a hook or
		// change the git configuration that the host will run later: .git is
		// laid over the writable mount read-only. The common git directory of
		// a linked worktree lies outside the repository and is never mounted.
		overlays, err := gitOverlays(repoRoot)
		if err != nil {
			return fail(fmt.Errorf("protecting .git: %w", err))
		}
		for _, rel := range overlays {
			mounts = append(mounts, dockerrun.Mount{
				Source: filepath.Join(repoRoot, filepath.FromSlash(rel)), Target: path.Join(ContainerRepoRoot, rel), ReadOnly: true,
			})
		}
	}
	for _, m := range settings.Mounts {
		source := filepath.Join(repoRoot, filepath.FromSlash(m.Path))
		if err := noSymlinkBelow(repoRoot, m.Path); err != nil {
			return fail(fmt.Errorf("mount %q: %w", m.Path, err))
		}
		mounts = append(mounts, dockerrun.Mount{Source: source, Target: m.Target, ReadOnly: !m.Writable})
	}
	stdin, err := json.Marshal(input)
	if err != nil {
		return fail(err)
	}

	workdir := ContainerRepoRoot
	if rel, ok := within(repoRoot, spec.Dir); ok && rel != "." {
		workdir = path.Join(ContainerRepoRoot, rel)
	}
	name, err := containerName(input.AttemptID)
	if err != nil {
		return fail(err)
	}
	dspec := dockerrun.Spec{
		Image: settings.Image, AllowUnpinned: settings.AllowUnpinned, Pull: settings.Pull,
		Name: name, Entrypoint: spec.Argv[0], Args: spec.Argv[1:], Workdir: workdir,
		User: l.user(settings), Mounts: mounts, TmpfsMB: settings.tmpfsMB(), Network: settings.network(),
		CPUs: settings.cpus(), MemoryMB: settings.memoryMB(), PidsLimit: settings.pidsLimit(),
		Env:    spec.Env,
		Labels: map[string]string{GateAttemptLabel: input.AttemptID},
	}
	described := dspec.Describe()
	described["exceptions"] = settings.Exceptions(envNames(spec.Env))
	// Host paths of the mounts are controller internals; the record keeps the
	// container side and, for repository mounts, the repository-relative path.
	described["mounts"] = describeMounts(repoRoot, evidenceDir, mounts)

	res := l.Docker.Run(ctx, dspec, stdin, spec.MaxOutputBytes)
	described["docker_argv"] = sanitizeDockerArgv(res.Docker, repoRoot, evidenceDir)
	return Outcome{
		Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode,
		OutputExceeded: res.OutputExceeded, Err: res.Err, Detail: res.Detail,
		Record: map[string]any{"container": described},
	}
}

// SweepOrphans removes gate containers left by a controller that died before it
// could remove them (see dockerrun.Runner.SweepOrphans). Best effort.
func (l DockerLauncher) SweepOrphans(ctx context.Context) ([]string, error) {
	return l.Docker.SweepOrphans(ctx, GateAttemptLabel, OrphanAge)
}

// gitOverlays returns the repository-relative paths that must be mounted
// read-only over a writable repository: .git (a directory, or the gitfile of a
// linked worktree) and every other git directory of the repository that lies
// inside it. A symlinked .git is refused: the daemon would follow it.
func gitOverlays(repoRoot string) ([]string, error) {
	overlays, err := topGitOverlays(repoRoot)
	if err != nil {
		return nil, err
	}
	nested, err := nestedGitOverlays(repoRoot, overlays)
	if err != nil {
		return nil, err
	}
	return append(overlays, nested...), nil
}

func topGitOverlays(repoRoot string) ([]string, error) {
	info, err := os.Lstat(filepath.Join(repoRoot, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !(info.IsDir() || info.Mode().IsRegular()) {
		return nil, errors.New(".git must be a directory or a regular file, not a symlink")
	}
	overlays := []string{".git"}
	if out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir").Output(); err == nil { // #nosec G204 -- fixed argv
		for _, dir := range strings.Fields(string(out)) {
			rel, ok := within(repoRoot, dir)
			if !ok || rel == "." || rel == ".git" {
				continue // outside the repository: not mounted at all
			}
			if inside, _ := within(filepath.Join(repoRoot, ".git"), dir); inside != "" && !strings.HasPrefix(inside, "..") {
				continue // already covered by the .git overlay
			}
			if err := noSymlinkBelow(repoRoot, rel); err != nil {
				return nil, fmt.Errorf("git directory %s: %w", rel, err)
			}
			overlays = append(overlays, rel)
		}
	}
	return overlays, nil
}

// maxNestedGitWalk bounds the search for nested git metadata.
const maxNestedGitWalk = 500000

// nestedGitOverlays finds git metadata below the repository root that a
// writable mount would otherwise expose: the `.git` directory of a nested
// clone, the gitfile of a submodule and the directory such a gitfile names when
// it lies inside the repository but outside an existing overlay. A symlinked
// nested `.git`, an unreadable or malformed gitfile are refused: the daemon
// would follow the link, and a program could plant hooks through them.
func nestedGitOverlays(repoRoot string, existing []string) ([]string, error) {
	covered := func(rel string) bool {
		for _, o := range existing {
			if rel == o || strings.HasPrefix(rel, o+"/") {
				return true
			}
		}
		return false
	}
	var found []string
	visited := 0
	err := filepath.WalkDir(repoRoot, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, ok := within(repoRoot, p)
		if !ok || rel == "." {
			return nil
		}
		if !strings.Contains("/"+rel+"/", "/node_modules/") {
			visited++ // node_modules is walked but not counted against the limit
		}
		if visited > maxNestedGitWalk {
			return fmt.Errorf("more than %d entries: nested git metadata cannot be checked", maxNestedGitWalk)
		}
		if strings.EqualFold(rel, ".git") {
			if entry.IsDir() {
				return filepath.SkipDir // the top-level git directory is covered already
			}
			return nil
		}
		if !strings.EqualFold(entry.Name(), ".git") || covered(rel) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("nested %s is a symlink", rel)
		}
		if err := noSymlinkBelow(repoRoot, rel); err != nil {
			return fmt.Errorf("nested git metadata %s: %w", rel, err)
		}
		found = append(found, rel)
		if entry.IsDir() {
			return filepath.SkipDir
		}
		target, err := gitfileTarget(p)
		if err != nil {
			return fmt.Errorf("gitfile %s: %w", rel, err)
		}
		targetRel, inside := within(repoRoot, target)
		if !inside || targetRel == "." || covered(targetRel) || containsPath(found, targetRel) {
			return nil // outside the repository it is not mounted; under .git it is read-only already
		}
		if err := noSymlinkBelow(repoRoot, targetRel); err != nil {
			return fmt.Errorf("gitfile %s names %s: %w", rel, targetRel, err)
		}
		found = append(found, targetRel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// hasGitElement reports whether a slash-separated repository path has a .git
// element (the top-level one or a nested clone or submodule).
func hasGitElement(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

func containsPath(list []string, rel string) bool {
	for _, item := range list {
		if item == rel {
			return true
		}
	}
	return false
}

// gitfileTarget returns the absolute directory a `gitdir: <path>` file names.
func gitfileTarget(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", errors.New("too large")
	}
	line := strings.TrimSpace(string(data))
	target, ok := strings.CutPrefix(line, "gitdir:")
	target = strings.TrimSpace(target)
	if !ok || target == "" || strings.ContainsAny(line, "\n\x00") {
		return "", errors.New(`not in the form "gitdir: <path>"`)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(file), filepath.FromSlash(target))
	}
	return filepath.Clean(target), nil
}

func (l DockerLauncher) user(settings DockerSettings) string {
	if settings.User != "" {
		return settings.User
	}
	uid, gid := os.Getuid(), os.Getgid()
	if l.Getuid != nil {
		uid = l.Getuid()
	}
	if l.Getgid != nil {
		gid = l.Getgid()
	}
	if uid == 0 || gid == 0 {
		return nobodyUser
	}
	if uid < 0 || gid < 0 {
		// Windows: no uid; the evidence directory is writable through the
		// Docker Desktop mapping for any user.
		return nobodyUser
	}
	return fmt.Sprintf("%d:%d", uid, gid)
}

func containerName(attemptID string) (string, error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	id := attemptID
	if len(id) > 64 {
		id = id[:64]
	}
	return "ai-team-gate-" + id + "-" + hex.EncodeToString(suffix[:]), nil
}

func envNames(env []string) []string {
	var names []string
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "AI_TEAM_GATE_") {
			names = append(names, name)
		}
	}
	return names
}

// within returns child relative to root (slash-separated) when child is inside
// root.
func within(root, child string) (string, bool) {
	rel, err := filepath.Rel(root, child)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// noSymlinkBelow proves that no component of rel below root is a symlink and
// that the final entry is a directory or a regular file. A container bind
// mount follows symlinks on the daemon side, so a repository symlink could
// otherwise expose any host path.
func noSymlinkBelow(root, rel string) error {
	current := root
	for _, part := range strings.Split(rel, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", part)
		}
	}
	info, err := os.Lstat(current)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("must be a directory or a regular file")
	}
	return nil
}

func noSymlinkFile(p string) error {
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("must be a regular file (no symlink)")
	}
	return nil
}

func describeMounts(repoRoot, evidenceDir string, mounts []dockerrun.Mount) []map[string]any {
	out := make([]map[string]any, len(mounts))
	for i, m := range mounts {
		source := "(external result file)"
		switch {
		case m.Source == repoRoot:
			source = "repository root"
		case m.Source == evidenceDir:
			source = "private evidence directory"
		default:
			if rel, ok := within(repoRoot, m.Source); ok {
				source = "repository:" + rel
			}
		}
		out[i] = map[string]any{"source": source, "target": m.Target, "read_only": m.ReadOnly}
	}
	return out
}

// sanitizeDockerArgv replaces controller host paths in the recorded docker
// argv by placeholders: the record is about what the container got, not about
// the controller's file system layout.
func sanitizeDockerArgv(argv []string, repoRoot, evidenceDir string) []string {
	out := make([]string, len(argv))
	for i, arg := range argv {
		arg = strings.ReplaceAll(arg, "source="+evidenceDir+",", "source=<evidence-dir>,")
		if strings.HasPrefix(arg, "type=bind,source="+repoRoot) {
			arg = strings.Replace(arg, "source="+repoRoot, "source=<repo>", 1)
		}
		if strings.Contains(arg, ",target="+ContainerArtifact+"/") {
			arg = "type=bind,source=<result-file>," + arg[strings.Index(arg, "target="):]
		}
		out[i] = arg
	}
	return out
}
