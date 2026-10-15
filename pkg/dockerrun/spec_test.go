package dockerrun

import (
	"strings"
	"testing"
)

const testImage = "alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

func validSpec() Spec {
	return Spec{
		Image: testImage, Name: "ai-team-gate-1", Entrypoint: "/bin/sh", Args: []string{"-c", "true"},
		Workdir: "/gate/repo", User: "1000:1000", Network: NetworkNone, CPUs: "1", MemoryMB: 256, PidsLimit: 64,
		TmpfsMB: 16,
		Mounts:  []Mount{{Source: "/repo", Target: "/gate/repo", ReadOnly: true}},
		Env:     []string{"TOKEN=very-secret-value-123"},
	}
}

func has(args []string, want ...string) bool {
	for i := range args {
		if i+len(want) <= len(args) && strings.Join(args[i:i+len(want)], "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

func TestRunArgsStrictDefaults(t *testing.T) {
	args, err := validSpec().RunArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{
		{"run"}, {"--rm"}, {"--read-only"}, {"--cap-drop", "ALL"}, {"--security-opt", "no-new-privileges"},
		{"--network", "none"}, {"--user", "1000:1000"}, {"--memory", "256m"}, {"--memory-swap", "256m"},
		{"--pids-limit", "64"}, {"--cpus", "1"}, {"--name", "ai-team-gate-1"}, {"--pull", "missing"}, {"-i"},
		{"--entrypoint", "/bin/sh", testImage, "-c", "true"},
		{"--mount", "type=bind,source=/repo,target=/gate/repo,readonly"},
		{"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=16m"},
	} {
		if !has(args, want...) {
			t.Errorf("argv lacks %v:\n%v", want, args)
		}
	}
	joined := strings.Join(args, " ")
	for _, banned := range []string{"docker.sock", "--privileged", "--network host", "--volume", " -v ", "--env-file", "very-secret-value-123", "--tty", " -t "} {
		if strings.Contains(joined, banned) {
			t.Errorf("argv contains %q: %s", banned, joined)
		}
	}
	if !has(args, "-e", "TOKEN") {
		t.Errorf("env must be passed by name only: %v", args)
	}
	// Exactly one --mount per declared mount, exactly one -e per declared variable.
	var mounts, envs int
	for _, a := range args {
		switch a {
		case "--mount":
			mounts++
		case "-e":
			envs++
		}
	}
	if mounts != 1 || envs != 1 {
		t.Errorf("mounts=%d envs=%d, want 1 and 1", mounts, envs)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Spec){
		"unpinned tag":       func(s *Spec) { s.Image = "alpine:3.20" },
		"latest":             func(s *Spec) { s.Image = "alpine" },
		"option as image":    func(s *Spec) { s.Image = "--privileged" },
		"uppercase":          func(s *Spec) { s.Image = "Alpine@sha256:" + strings.Repeat("a", 64) },
		"short digest":       func(s *Spec) { s.Image = "alpine@sha256:abc" },
		"host network":       func(s *Spec) { s.Network = "host" },
		"empty network":      func(s *Spec) { s.Network = "" },
		"no user":            func(s *Spec) { s.User = "" },
		"user injection":     func(s *Spec) { s.User = "0 --privileged" },
		"relative workdir":   func(s *Spec) { s.Workdir = "gate" },
		"mount comma":        func(s *Spec) { s.Mounts[0].Source = "/repo,source=/" },
		"mount relative":     func(s *Spec) { s.Mounts[0].Source = "repo" },
		"mount root target":  func(s *Spec) { s.Mounts[0].Target = "/" },
		"mount proc":         func(s *Spec) { s.Mounts[0].Target = "/proc/x" },
		"duplicate target":   func(s *Spec) { s.Mounts = append(s.Mounts, Mount{Source: "/o", Target: "/gate/repo"}) },
		"tmp twice":          func(s *Spec) { s.Mounts = append(s.Mounts, Mount{Source: "/o", Target: "/tmp"}) },
		"client env HOME":    func(s *Spec) { s.Env = []string{"HOME=/x"} },
		"client env DOCKER_": func(s *Spec) { s.Env = []string{"DOCKER_HOST=tcp://x"} },
		"env bad name":       func(s *Spec) { s.Env = []string{"A-B=1"} },
		"env without value":  func(s *Spec) { s.Env = []string{"TOKEN"} },
		"env twice":          func(s *Spec) { s.Env = []string{"A=1", "A=2"} },
		"cpus text":          func(s *Spec) { s.CPUs = "all" },
		"cpus zero":          func(s *Spec) { s.CPUs = "0" },
		"memory zero":        func(s *Spec) { s.MemoryMB = 0 },
		"pids zero":          func(s *Spec) { s.PidsLimit = 0 },
		"no entrypoint":      func(s *Spec) { s.Entrypoint = "" },
		"bad name":           func(s *Spec) { s.Name = "a b" },
		"pull always":        func(s *Spec) { s.Pull = "always" },
		"label injection":    func(s *Spec) { s.Labels = map[string]string{"A=B": "x"} },
	}
	for name, mutate := range cases {
		spec := validSpec()
		spec.Mounts = append([]Mount(nil), spec.Mounts...)
		mutate(&spec)
		err := spec.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "very-secret-value-123") {
			t.Errorf("%s: error echoes a secret: %v", name, err)
		}
	}
	spec := validSpec()
	spec.Image = "alpine:3.20"
	spec.AllowUnpinned = true
	if err := spec.Validate(); err != nil {
		t.Errorf("explicit allow_unpinned: %v", err)
	}
}

func TestDescribeHasNoValues(t *testing.T) {
	record := validSpec().Describe()
	if record["docker_socket"] != false || record["host_home"] != false || record["network"] != NetworkNone {
		t.Fatalf("record = %v", record)
	}
	if strings.Contains(strings.Join(record["env_names"].([]string), ","), "secret") {
		t.Fatalf("record leaks a value: %v", record)
	}
}
