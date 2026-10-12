package prchecks

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/delivery"
)

var scpRemote = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9.-]+):([^/].*)$`)

// TaskRepository returns the lower-cased "host/owner/repo" identity of the
// task checkout's origin remote. The controller calls it once at run start,
// before any agent can touch the checkout, and persists the value; PR checks
// later compare every PR against that captured identity instead of asking gh
// to infer a repository from mutable Git configuration.
//
// It fails closed when Git configuration could redirect the identity: more
// than one origin URL, a URL rewrite (insteadOf/pushInsteadOf) matching
// origin, or a gh-resolved remote hint that would make gh pick another repo.
func (r Runner) TaskRepository(ctx context.Context, targetDir string) (string, error) {
	result := r.commands().Run(ctx, targetDir, "git", "config", "--null", "--list")
	if result.Status != delivery.StepPassed {
		return "", commandError("git config --list", result)
	}
	if result.Truncated {
		return "", errors.New("git config output was truncated")
	}
	return r.taskRepositoryFromConfig(result.Stdout)
}

func (r Runner) taskRepositoryFromConfig(configOutput string) (string, error) {
	if configOutput != "" && !strings.HasSuffix(configOutput, "\x00") {
		return "", errors.New("git configuration could not be safely parsed")
	}
	type rewrite struct{ key, prefix string }
	var origins []string
	var rewrites []rewrite
	for _, record := range strings.Split(strings.TrimSuffix(configOutput, "\x00"), "\x00") {
		if record == "" {
			continue
		}
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			// A key without a value (boolean shorthand) cannot be a URL.
			key, value = record, ""
		}
		lowerKey := strings.ToLower(key)
		switch {
		case lowerKey == "remote.origin.url":
			origins = append(origins, value)
		case strings.HasPrefix(lowerKey, "remote.") && strings.HasSuffix(lowerKey, ".gh-resolved"):
			return "", fmt.Errorf("git configuration sets %s; gh remote resolution overrides are not allowed for PR checks", key)
		case strings.HasPrefix(lowerKey, "url.") && (strings.HasSuffix(lowerKey, ".insteadof") || strings.HasSuffix(lowerKey, ".pushinsteadof")):
			if strings.ContainsAny(value, "\r\n") {
				return "", errors.New("git URL rewrite configuration could not be safely parsed")
			}
			rewrites = append(rewrites, rewrite{key: key, prefix: value})
		}
	}
	if len(origins) != 1 {
		return "", fmt.Errorf("origin remote must have exactly one URL, found %d", len(origins))
	}
	origin := origins[0]
	for _, candidate := range rewrites {
		if candidate.prefix != "" && strings.HasPrefix(origin, candidate.prefix) {
			return "", fmt.Errorf("git configuration rewrites the origin URL via %s", candidate.key)
		}
	}
	host, repository, err := remoteRepositoryIdentity(origin)
	if err != nil {
		return "", fmt.Errorf("origin is not a GitHub repository URL: %w", err)
	}
	trusted, err := r.trustedHost("https://" + host + "/")
	if err != nil {
		return "", fmt.Errorf("origin host: %w", err)
	}
	return trusted + "/" + repository, nil
}

// remoteRepositoryIdentity parses https, ssh and scp-style Git remotes.
func remoteRepositoryIdentity(remote string) (host, repository string, err error) {
	remote = strings.TrimSpace(remote)
	var path string
	if strings.Contains(remote, "://") {
		parsed, parseErr := url.Parse(remote)
		if parseErr != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", errors.New("malformed remote URL")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "https":
			if parsed.User != nil {
				return "", "", errors.New("HTTPS remote with embedded credentials")
			}
			host = parsed.Host
		case "ssh":
			host = parsed.Hostname()
		default:
			return "", "", fmt.Errorf("unsupported remote scheme %q", parsed.Scheme)
		}
		path = parsed.Path
	} else if match := scpRemote.FindStringSubmatch(remote); match != nil {
		host, path = match[1], match[2]
	} else {
		return "", "", errors.New("unsupported remote form")
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || strings.TrimSuffix(parts[1], ".git") == "" {
		return "", "", fmt.Errorf("remote path %q is not owner/repo", path)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host, strings.ToLower(parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")), nil
}
