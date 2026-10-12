package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults for the controller-owned deploy wait.
const (
	DefaultDeployTimeout      = 30 * time.Minute
	DefaultDeployPollInterval = 15 * time.Second
	DefaultMergeTimeout       = 30 * time.Minute
	// DeployStageMargin covers Git fetches, gh calls and the build cancel
	// request on top of the merge and build waits.
	DeployStageMargin = 5 * time.Minute
)

// CICDConfig configures the controller-owned CI/CD provider used by deploy
// stages. Credentials are referenced only by environment variable name.
type CICDConfig struct {
	TeamCity *TeamCityConfig `yaml:"teamcity,omitempty"`
}

// TeamCityConfig selects the TeamCity server and the build configuration that
// builds and deploys a merged commit of Branch.
type TeamCityConfig struct {
	BaseURL      string `yaml:"base_url"`
	BuildType    string `yaml:"build_type"`
	TokenEnv     string `yaml:"token_env"`
	Branch       string `yaml:"branch"`
	Timeout      string `yaml:"timeout,omitempty"`
	MergeTimeout string `yaml:"merge_timeout,omitempty"`
	PollInterval string `yaml:"poll_interval,omitempty"`
}

var (
	teamCityBuildTypeName = regexp.MustCompile(`^[A-Za-z0-9_]{1,225}$`)
	deployBranchName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

func (cc *CICDConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{"teamcity": true}, "config: cicd"); err != nil {
		return err
	}
	type plain CICDConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*cc = CICDConfig(decoded)
	return nil
}

func (tc *TeamCityConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"base_url": true, "build_type": true, "token_env": true, "branch": true,
		"timeout": true, "merge_timeout": true, "poll_interval": true,
	}, "config: cicd teamcity"); err != nil {
		return err
	}
	type plain TeamCityConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*tc = TeamCityConfig(decoded)
	return nil
}

func (cc *CICDConfig) Validate() error {
	if cc == nil {
		return nil
	}
	if cc.TeamCity == nil {
		return fmt.Errorf("cicd must configure teamcity")
	}
	return cc.TeamCity.Validate()
}

func (tc *TeamCityConfig) Validate() error {
	var problems []string
	parsed, err := url.Parse(strings.TrimSpace(tc.BaseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		problems = append(problems, "cicd.teamcity.base_url должен быть абсолютным HTTPS URL без credentials, query и fragment")
	}
	if !teamCityBuildTypeName.MatchString(tc.BuildType) {
		problems = append(problems, "cicd.teamcity.build_type должен быть ID build configuration TeamCity")
	}
	if !envVariableName.MatchString(tc.TokenEnv) {
		problems = append(problems, "cicd.teamcity.token_env должен быть именем переменной окружения")
	}
	if !deployBranchName.MatchString(tc.Branch) || strings.Contains(tc.Branch, "..") || strings.Contains(tc.Branch, "//") ||
		strings.HasSuffix(tc.Branch, "/") || strings.HasSuffix(tc.Branch, ".") || strings.HasSuffix(tc.Branch, ".lock") {
		problems = append(problems, "cicd.teamcity.branch должен быть именем ветки, в которую мержится PR")
	}
	if _, err := tc.EffectiveTimeout(); err != nil {
		problems = append(problems, err.Error())
	}
	if _, err := tc.EffectiveMergeTimeout(); err != nil {
		problems = append(problems, err.Error())
	}
	if _, err := tc.EffectivePollInterval(); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// EffectiveTimeout returns the configured wait budget or the default.
func (tc *TeamCityConfig) EffectiveTimeout() (time.Duration, error) {
	if tc == nil || strings.TrimSpace(tc.Timeout) == "" {
		return DefaultDeployTimeout, nil
	}
	value, err := time.ParseDuration(tc.Timeout)
	if err != nil || value <= 0 || value > 24*time.Hour {
		return 0, fmt.Errorf("cicd.teamcity.timeout %q должен быть положительной длительностью не больше 24h", tc.Timeout)
	}
	return value, nil
}

// EffectivePollInterval returns the configured status poll interval or the default.
func (tc *TeamCityConfig) EffectivePollInterval() (time.Duration, error) {
	if tc == nil || strings.TrimSpace(tc.PollInterval) == "" {
		return DefaultDeployPollInterval, nil
	}
	value, err := time.ParseDuration(tc.PollInterval)
	if err != nil || value < time.Second || value > 10*time.Minute {
		return 0, fmt.Errorf("cicd.teamcity.poll_interval %q должен быть от 1s до 10m", tc.PollInterval)
	}
	return value, nil
}

// EffectiveMergeTimeout bounds how long a deploy stage waits for the pull
// request merge. Zero means a single check without waiting.
func (tc *TeamCityConfig) EffectiveMergeTimeout() (time.Duration, error) {
	if tc == nil || strings.TrimSpace(tc.MergeTimeout) == "" {
		return DefaultMergeTimeout, nil
	}
	value, err := time.ParseDuration(tc.MergeTimeout)
	if err != nil || value < 0 || value > 24*time.Hour {
		return 0, fmt.Errorf("cicd.teamcity.merge_timeout %q должен быть длительностью от 0 до 24h", tc.MergeTimeout)
	}
	return value, nil
}

// StageBudget is the time budget of a controller deploy stage: merge wait,
// build wait and a fixed margin. It replaces stage_timeout for that stage so
// a slow merge can never cancel a running deployment.
func (tc *TeamCityConfig) StageBudget() (time.Duration, error) {
	mergeTimeout, err := tc.EffectiveMergeTimeout()
	if err != nil {
		return 0, err
	}
	buildTimeout, err := tc.EffectiveTimeout()
	if err != nil {
		return 0, err
	}
	return mergeTimeout + buildTimeout + DeployStageMargin, nil
}
