//go:build !windows

package dockerrun

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSweepOrphansWithRealDocker starts three idle containers the way a dead
// controller would have left them (one old, one fresh, one of another owner)
// and checks that only the old one of our label is swept.
func TestSweepOrphansWithRealDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unavailable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "pull", "-q", testImage).CombinedOutput(); err != nil {
		t.Skipf("pinned test image cannot be pulled: %v: %s", err, strings.TrimSpace(string(out)))
	}
	const key = "ai-team.s08test"
	start := func(name string, labels ...string) {
		t.Helper()
		args := []string{"run", "-d", "--name", name, "--network", "none"}
		for _, l := range labels {
			args = append(args, "--label", l)
		}
		args = append(args, testImage, "sleep", "120")
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	oldName, freshName, foreignName := "s08sweep-old-"+suffix, "s08sweep-fresh-"+suffix, "s08sweep-foreign-"+suffix
	old := strconv.FormatInt(time.Now().Add(-3*time.Hour).Unix(), 10)
	now := strconv.FormatInt(time.Now().Unix(), 10)
	start(oldName, LabelOwner+"=1", key+"=x", LabelStarted+"="+old)
	start(freshName, LabelOwner+"=1", key+"=x", LabelStarted+"="+now)
	start(foreignName, key+"=x", LabelStarted+"="+old) // not ours: no owner label

	removed, err := Runner{}.SweepOrphans(ctx, key, time.Hour)
	if err != nil || len(removed) != 1 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	exists := func(name string) bool {
		out, _ := exec.Command("docker", "ps", "-a", "-q", "--filter", "name=^"+name+"$").Output()
		return strings.TrimSpace(string(out)) != ""
	}
	if exists(oldName) || !exists(freshName) || !exists(foreignName) {
		t.Fatalf("old exists=%v fresh exists=%v foreign exists=%v", exists(oldName), exists(freshName), exists(foreignName))
	}
}
