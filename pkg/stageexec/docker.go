package stageexec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/dockerrun"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
	"gopkg.in/yaml.v3"
)

// Container paths of the Docker stage executor. The program sees only these
// paths in the JSON request; host paths never leave the controller.
const (
	ContainerRoot    = "/stage"
	ContainerInputs  = "/stage/in"
	ContainerOutputs = "/stage/out"
	ContainerRepo    = "/stage/repo"
)

// Defaults of a container executor. Every value that widens access is an
// explicit setting and is recorded in the attempt metadata.
const (
	DefaultDockerCPUs      = "1"
	DefaultDockerMemoryMB  = 512
	DefaultDockerPidsLimit = 128
	DefaultDockerTmpfsMB   = 64
	// DefaultDockerOutputMB caps everything the program may write to /stage/out.
	DefaultDockerOutputMB = 64
	// MaxDockerOutputMB is the largest value output_mb accepts.
	MaxDockerOutputMB = 1024
	nobodyUser        = "65534:65534"
	// maxOutputFileBytes caps one result file the program writes to /stage/out.
	maxOutputFileBytes = 16 << 20
)

// StageAttemptLabel marks every stage container with its attempt ID; the
// start-up sweep finds leftovers of a dead controller by it.
const StageAttemptLabel = "ai-team.stage-attempt"

// OrphanAge is how old a stage container must be before the sweep removes it:
// longer than the longest allowed executor run, so a container of another live
// controller on the same daemon is never touched.
const OrphanAge = maxTimeout + 15*time.Minute

var (
	inputNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	cpusPattern      = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,2})?$`)
)

// DockerSpec is the container half of an executor adapter. Zero values mean the
// strict defaults.
type DockerSpec struct {
	// Image must be name@sha256:<digest> unless AllowUnpinnedImage is set.
	Image              string `yaml:"image"`
	AllowUnpinnedImage bool   `yaml:"allow_unpinned_image,omitempty"`
	// Pull is "" (docker default, missing) or "never".
	Pull string `yaml:"pull,omitempty"`
	// Network is "" or none (default) or bridge.
	Network   string `yaml:"network,omitempty"`
	CPUs      string `yaml:"cpus,omitempty"`
	MemoryMB  int    `yaml:"memory_mb,omitempty"`
	PidsLimit int    `yaml:"pids_limit,omitempty"`
	// TmpfsMB is the size of the scratch /tmp; nil means the default, 0 none.
	TmpfsMB *int `yaml:"tmpfs_mb,omitempty"`
	// OutputMB caps the total size of /stage/out (default 64). The directory is
	// a host bind mount, so the controller watches its size while the container
	// runs and kills the container on overflow; 0 means the default.
	OutputMB int `yaml:"output_mb,omitempty"`
	// User is uid[:gid] or a name; empty means the controller's uid:gid (nobody
	// when the controller is root).
	User string `yaml:"user,omitempty"`
	// Repo is "" (the repository is not mounted) or "ro" (read-only at
	// /stage/repo).
	Repo string `yaml:"repo,omitempty"`
	// Mounts are extra read-only repository files or directories for the
	// program itself, written "<repository path>:<container path>".
	Mounts []string `yaml:"mounts,omitempty"`
}

// UnmarshalYAML rejects unknown keys: a typo in a limit would otherwise run the
// container without that limit.
func (d *DockerSpec) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.MappingNode {
		allowed := map[string]bool{"image": true, "allow_unpinned_image": true, "pull": true, "network": true,
			"cpus": true, "memory_mb": true, "pids_limit": true, "tmpfs_mb": true, "output_mb": true, "user": true, "repo": true, "mounts": true}
		for index := 0; index+1 < len(node.Content); index += 2 {
			if key := node.Content[index].Value; !allowed[key] {
				return fmt.Errorf("config: executor adapter docker: неизвестный ключ %q", key)
			}
		}
	}
	type plain DockerSpec
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*d = DockerSpec(decoded)
	return nil
}

func (d DockerSpec) network() string {
	if d.Network == "" {
		return dockerrun.NetworkNone
	}
	return d.Network
}

func (d DockerSpec) cpus() string {
	if d.CPUs == "" {
		return DefaultDockerCPUs
	}
	return d.CPUs
}

func (d DockerSpec) memoryMB() int {
	if d.MemoryMB == 0 {
		return DefaultDockerMemoryMB
	}
	return d.MemoryMB
}

func (d DockerSpec) pidsLimit() int {
	if d.PidsLimit == 0 {
		return DefaultDockerPidsLimit
	}
	return d.PidsLimit
}

func (d DockerSpec) tmpfsMB() int {
	if d.TmpfsMB == nil {
		return DefaultDockerTmpfsMB
	}
	return *d.TmpfsMB
}

func (d DockerSpec) outputBytes() int64 {
	if d.OutputMB == 0 {
		return DefaultDockerOutputMB << 20
	}
	return int64(d.OutputMB) << 20
}

type dockerMount struct{ Path, Target string }

func parseDockerMount(spec string) (dockerMount, error) {
	source, target, ok := strings.Cut(spec, ":")
	if !ok || source == "" || target == "" || strings.Contains(target, ":") {
		return dockerMount{}, fmt.Errorf("%q: ожидается <путь в репозитории>:<путь в контейнере> (только чтение)", spec)
	}
	return dockerMount{Path: source, Target: target}, nil
}

func relRepoPath(p string) error {
	clean := path.Clean(p)
	if p == "" || p != clean || path.IsAbs(p) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(p, "\\\x00") {
		return errors.New("ожидается чистый путь внутри репозитория без ..")
	}
	return nil
}

// Validate checks the settings with the rules dockerrun enforces at launch
// time, so a bad configuration fails before any run starts.
func (d DockerSpec) Validate() error {
	probe := dockerrun.Spec{
		Image: d.Image, AllowUnpinned: d.AllowUnpinnedImage, Pull: d.Pull, Name: "validate",
		Entrypoint: "x", Workdir: ContainerRoot, User: orUser(d.User),
		Network: d.network(), CPUs: d.cpus(), MemoryMB: d.memoryMB(), PidsLimit: d.pidsLimit(), TmpfsMB: d.tmpfsMB(),
	}
	if d.OutputMB < 0 || d.OutputMB > MaxDockerOutputMB {
		return fmt.Errorf("output_mb %d: допустимо от 0 до %d", d.OutputMB, MaxDockerOutputMB)
	}
	if d.CPUs != "" && !cpusPattern.MatchString(d.CPUs) {
		return fmt.Errorf("cpus %q: ожидается десятичное число, например 1 или 0.5", d.CPUs)
	}
	switch d.Repo {
	case "", "ro":
	default:
		return fmt.Errorf("repo %q: допустимо только ro или отсутствие поля", d.Repo)
	}
	for _, spec := range d.Mounts {
		mount, err := parseDockerMount(spec)
		if err != nil {
			return fmt.Errorf("mounts: %w", err)
		}
		if err := relRepoPath(mount.Path); err != nil {
			return fmt.Errorf("mounts %q: %w", spec, err)
		}
		if mount.Target == ContainerRoot || strings.HasPrefix(mount.Target, ContainerRoot+"/") {
			return fmt.Errorf("mounts %q: каталог %s зарезервирован контроллером", spec, ContainerRoot)
		}
		probe.Mounts = append(probe.Mounts, dockerrun.Mount{Source: "/probe/" + mount.Path, Target: mount.Target, ReadOnly: true})
	}
	return probe.Validate()
}

// Exceptions lists, in words, everything that is wider than the strict
// defaults. It names paths and variable names, never values.
func (d DockerSpec) Exceptions(envNames []string) []string {
	var out []string
	if d.network() != dockerrun.NetworkNone {
		out = append(out, "network="+d.network())
	}
	if d.Repo == "ro" {
		out = append(out, "repository mounted read-only")
	}
	for _, spec := range d.Mounts {
		out = append(out, "extra read-only mount "+spec)
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
	if d.AllowUnpinnedImage && !dockerrun.IsPinned(d.Image) {
		out = append(out, "image is not pinned by digest")
	}
	return out
}

func orUser(user string) string {
	if user == "" {
		return "1000:1000"
	}
	return user
}

var sweepOnce sync.Once

// DockerLauncher runs the executor program in a container through the docker
// CLI and speaks the same JSON contract as the host launcher. It mounts only
// what the stage declared: every input read-only, one private result directory
// read-write, and nothing else of the controller.
type DockerLauncher struct {
	Settings DockerSpec
	// Docker is the container primitive; the zero value uses the docker CLI.
	Docker dockerrun.Runner
	// Getuid and Getgid default to the controller's ids; they choose the
	// container user when the settings name none.
	Getuid, Getgid func() int
}

// SweepOrphans removes stage containers left by a controller that died before
// it could remove them. Best effort.
func (l DockerLauncher) SweepOrphans(ctx context.Context) ([]string, error) {
	return l.Docker.SweepOrphans(ctx, StageAttemptLabel, OrphanAge)
}

type launchOutputs struct {
	Status  string                     `json:"status"`
	Outputs map[string]json.RawMessage `json:"outputs"`
}

// Run implements Launcher.
func (l DockerLauncher) Run(ctx context.Context, launch Launch) error {
	sweepOnce.Do(func() { _, _ = l.SweepOrphans(ctx) })
	if len(launch.Argv) == 0 {
		return errors.New("docker executor: пустой argv")
	}
	settings := l.Settings
	request := launch.Request

	doc := newRequestDocument(request)
	doc.Outputs = append([]Output(nil), doc.Outputs...) // the request keeps its host paths
	mounts := make([]dockerrun.Mount, 0, len(request.Inputs)+len(settings.Mounts)+2)
	described := make([]string, 0, cap(mounts))
	for index, input := range request.Inputs {
		if !inputNamePattern.MatchString(input.Name) {
			return fmt.Errorf("docker executor: имя входа %q нельзя безопасно передать в контейнер", input.Name)
		}
		source, err := filepath.Abs(input.Path)
		if err != nil {
			return fmt.Errorf("docker executor: вход %q: %w", input.Name, err)
		}
		if err := noSymlinkEntry(source); err != nil {
			return fmt.Errorf("docker executor: вход %q: %w", input.Name, err)
		}
		target := ContainerInputs + "/" + input.Name
		mounts = append(mounts, dockerrun.Mount{Source: source, Target: target, ReadOnly: true})
		described = append(described, "in/"+input.Name+":ro")
		doc.Inputs[index].Path = target
	}
	for index, output := range doc.Outputs {
		if !inputNamePattern.MatchString(output.Name) {
			return fmt.Errorf("docker executor: имя выхода %q нельзя безопасно передать в контейнер", output.Name)
		}
		doc.Outputs[index].Path = ContainerOutputs + "/" + output.Name
	}
	workdir := ContainerRoot
	if settings.Repo == "ro" {
		repo, err := filepath.Abs(request.WorkDir)
		if err != nil {
			return fmt.Errorf("docker executor: workdir: %w", err)
		}
		mounts = append(mounts, dockerrun.Mount{Source: repo, Target: ContainerRepo, ReadOnly: true})
		described = append(described, "repo:ro")
		workdir = ContainerRepo
	}
	for _, spec := range settings.Mounts {
		mount, err := parseDockerMount(spec)
		if err != nil {
			return fmt.Errorf("docker executor: %w", err)
		}
		if err := noSymlinkBelow(request.WorkDir, mount.Path); err != nil {
			return fmt.Errorf("docker executor: mount %q: %w", mount.Path, err)
		}
		mounts = append(mounts, dockerrun.Mount{Source: filepath.Join(request.WorkDir, filepath.FromSlash(mount.Path)), Target: mount.Target, ReadOnly: true})
		described = append(described, mount.Target+":ro")
	}
	// The container paths replace the host layout in the request.
	doc.WorkDir, doc.ArtifactRoot = workdir, ContainerInputs

	// The scratch root is private (0700, owner only). The mounted result
	// directory lives inside it: with the default user (the controller's own
	// uid) it is 0700 as well; an explicit user need not be that uid, so the
	// directory is opened to it (0777) but stays unreachable for every other
	// host account because its parent is private. It is scratch space: the
	// collected files are copied to their declared paths and the root is removed.
	scratch, err := os.MkdirTemp("", "ai-team-stage-")
	if err != nil {
		return fmt.Errorf("docker executor: каталог результата: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	resultDir := filepath.Join(scratch, "out")
	mode := os.FileMode(0o700)
	if settings.User != "" {
		mode = 0o777
	}
	if err := os.Mkdir(resultDir, 0o700); err != nil {
		return fmt.Errorf("docker executor: каталог результата: %w", err)
	}
	if err := os.Chmod(resultDir, mode); err != nil { // Mkdir is subject to umask
		return fmt.Errorf("docker executor: каталог результата: %w", err)
	}
	mounts = append(mounts, dockerrun.Mount{Source: resultDir, Target: ContainerOutputs, ReadOnly: false})
	described = append(described, "out:rw")

	stdin, err := jsonMarshal(doc)
	if err != nil {
		return fmt.Errorf("docker executor: сериализация запроса: %w", err)
	}
	name, err := containerName(request)
	if err != nil {
		return err
	}
	var env []string
	var envNames []string
	for _, entry := range launch.Env {
		variable, _, _ := strings.Cut(entry, "=")
		if variable == "PATH" {
			continue // the container keeps the PATH of its image
		}
		env = append(env, entry)
		if !strings.HasPrefix(variable, reservedEnvPrefix) {
			envNames = append(envNames, variable)
		}
	}
	spec := dockerrun.Spec{
		Image: settings.Image, AllowUnpinned: settings.AllowUnpinnedImage, Pull: settings.Pull,
		Name: name, Entrypoint: launch.Argv[0], Args: launch.Argv[1:], Workdir: workdir,
		User: l.user(settings), Mounts: mounts, TmpfsMB: settings.tmpfsMB(), Network: settings.network(),
		CPUs: settings.cpus(), MemoryMB: settings.memoryMB(), PidsLimit: settings.pidsLimit(),
		Env: env, Labels: map[string]string{StageAttemptLabel: request.AttemptID},
	}
	if launch.Provenance != nil {
		sort.Strings(described)
		for key, value := range map[string]string{
			"container.image": settings.Image, "container.network": settings.network(), "container.user": spec.User,
			"container.cpus": spec.CPUs, "container.memory_mb": strconv.Itoa(spec.MemoryMB),
			"container.pids_limit": strconv.Itoa(spec.PidsLimit), "container.name": name,
			"container.mounts":     truncateString(strings.Join(described, ","), maxMetadataValueBytes),
			"container.exceptions": truncateString(strings.Join(settings.Exceptions(envNames), "; "), maxMetadataValueBytes),
		} {
			launch.Provenance[key] = value
		}
		if dockerrun.IsPinned(settings.Image) {
			launch.Provenance["container.image_pinned"] = "true"
		} else {
			launch.Provenance["container.image_pinned"] = "false"
		}
	}

	limit := launch.Stdout.Limit()
	if other := launch.Stderr.Limit(); other > limit {
		limit = other
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	exceeded := make(chan int64, 1)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		watchDirSize(runCtx, resultDir, settings.outputBytes(), func(size int64) {
			exceeded <- size
			cancelRun()
		})
	}()
	res := l.Docker.Run(runCtx, spec, stdin, limit)
	cancelRun()
	<-watched
	select {
	case size := <-exceeded:
		_, _ = launch.Stdout.Write(res.Stdout)
		_, _ = launch.Stderr.Write(res.Stderr)
		return fmt.Errorf("каталог результата %s превысил лимит %d МБ (записано не менее %d байт): контейнер остановлен", ContainerOutputs, settings.outputBytes()>>20, size)
	default:
	}
	_, _ = launch.Stdout.Write(res.Stdout)
	_, _ = launch.Stderr.Write(res.Stderr)
	if res.Err != nil {
		message := fmt.Sprintf("контейнер завершился с ошибкой: %v", res.Err)
		if res.OutputExceeded {
			message += "; вывод превысил лимит"
		}
		if res.Detail != "" {
			message += "; " + res.Detail
		}
		return errors.New(message)
	}
	// Result files are only read here, never written to their declared paths:
	// External.Execute commits them after the whole response passed the
	// contract check, so a rejected answer leaves nothing behind.
	return stageResultFiles(request, resultDir, res.Stdout, launch.StagedFiles)
}

// watchDirSize polls the size of dir and calls onExceed once when it passes
// limit. It returns when ctx ends or the limit is exceeded.
func watchDirSize(ctx context.Context, dir string, limit int64, onExceed func(size int64)) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if size := dirSize(dir, limit); size > limit {
				onExceed(size)
				return
			}
		}
	}
}

// dirSize sums regular-file sizes below dir without following links and stops
// early once the sum passes limit.
func dirSize(dir string, limit int64) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // the program may delete files while we walk
		}
		if info, infoErr := entry.Info(); infoErr == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		if total > limit {
			return fs.SkipAll
		}
		return nil
	})
	return total
}

// stageResultFiles reads the files the program wrote to /stage/out into staged,
// keyed by output name, only when the program reported status ok. A name given
// both as content and as a file is ambiguous and fails the attempt. Nothing is
// written to a declared path here.
func stageResultFiles(request Request, resultDir string, stdout []byte, staged map[string][]byte) error {
	var reported launchOutputs
	if json.Unmarshal(stdout, &reported) != nil || reported.Status != "ok" {
		return nil // the contract check reports the real problem
	}
	for _, output := range request.Outputs {
		source := filepath.Join(resultDir, output.Name)
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("выход %q: %w", output.Name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("выход %q: в каталоге результата не обычный файл", output.Name)
		}
		if _, both := reported.Outputs[output.Name]; both {
			return fmt.Errorf("выход %q: задан и как content, и как файл (неоднозначный ответ)", output.Name)
		}
		data, err := safeio.ReadRegularFile(source, maxOutputFileBytes)
		if err != nil {
			return fmt.Errorf("выход %q: %w", output.Name, err)
		}
		if staged != nil {
			staged[output.Name] = data
		}
	}
	return nil
}

// writeStagedFiles writes files staged by a launcher to the declared output
// paths. It runs only after the response passed the contract check.
func writeStagedFiles(request Request, staged map[string][]byte) error {
	for _, output := range request.Outputs {
		data, ok := staged[output.Name]
		if !ok {
			continue
		}
		if err := safeio.EnsureDirPath(filepath.Dir(output.Path)); err != nil {
			return fmt.Errorf("выход %q: каталог: %w", output.Name, err)
		}
		if err := safeio.ReplaceRegularFileNoFollow(output.Path, data, 0o644); err != nil {
			return fmt.Errorf("выход %q: запись: %w", output.Name, err)
		}
	}
	return nil
}

func (l DockerLauncher) user(settings DockerSpec) string {
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
	if uid <= 0 || gid <= 0 {
		return nobodyUser // never root implicitly; Windows has no uid
	}
	return fmt.Sprintf("%d:%d", uid, gid)
}

var containerIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func containerName(request Request) (string, error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	id := request.AttemptID
	if id == "" {
		id = request.Stage
	}
	id = containerIDUnsafe.ReplaceAllString(id, "-")
	if len(id) > 64 {
		id = id[:64]
	}
	return "ai-team-stage-" + id + "-" + hex.EncodeToString(suffix[:]), nil
}

// noSymlinkEntry rejects a mount source that is a symlink or neither a regular
// file nor a directory: a bind mount follows symlinks on the daemon side.
func noSymlinkEntry(p string) error {
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("символическая ссылка не монтируется")
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("ожидается обычный файл или каталог")
	}
	return nil
}

// noSymlinkBelow proves that no component of rel below root is a symlink.
func noSymlinkBelow(root, rel string) error {
	current := root
	for _, part := range strings.Split(rel, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: символическая ссылка", part)
		}
	}
	return noSymlinkEntry(current)
}
