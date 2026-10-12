package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestCICDTeamCityConfigParsesAndValidates(t *testing.T) {
	var cfg struct {
		CICD *CICDConfig `yaml:"cicd"`
	}
	err := yaml.Unmarshal([]byte(`cicd:
  teamcity:
    base_url: https://teamcity.example.test/tc
    build_type: Project_DeployProd
    token_env: TEAMCITY_TOKEN
    branch: main
    timeout: 20m
    poll_interval: 10s
`), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CICD == nil || cfg.CICD.TeamCity == nil || cfg.CICD.Validate() != nil {
		t.Fatalf("valid cicd config rejected: %+v", cfg.CICD)
	}
	if timeout, _ := cfg.CICD.TeamCity.EffectiveTimeout(); timeout != 20*time.Minute {
		t.Fatalf("timeout = %s", timeout)
	}
	if err := yaml.Unmarshal([]byte("cicd:\n  teamcity:\n    token: secret\n"), &cfg); err == nil {
		t.Fatal("inline token field must be rejected")
	}
	for name, mutate := range map[string]func(*TeamCityConfig){
		"http":          func(c *TeamCityConfig) { c.BaseURL = "http://teamcity.example.test" },
		"credentials":   func(c *TeamCityConfig) { c.BaseURL = "https://u:p@teamcity.example.test" },
		"build type":    func(c *TeamCityConfig) { c.BuildType = "a/b" },
		"token env":     func(c *TeamCityConfig) { c.TokenEnv = "not-an-env" },
		"branch":        func(c *TeamCityConfig) { c.Branch = "-main" },
		"timeout":       func(c *TeamCityConfig) { c.Timeout = "-1m" },
		"poll interval": func(c *TeamCityConfig) { c.PollInterval = "10ms" },
	} {
		valid := *cfg.CICD.TeamCity
		mutate(&valid)
		if err := (&CICDConfig{TeamCity: &valid}).Validate(); err == nil {
			t.Fatalf("%s: invalid config accepted", name)
		}
	}
	if err := (&CICDConfig{}).Validate(); err == nil {
		t.Fatal("cicd without provider must be rejected")
	}
}

func TestTeamCityMergeTimeout(t *testing.T) {
	config := &TeamCityConfig{}
	if value, err := config.EffectiveMergeTimeout(); err != nil || value != DefaultMergeTimeout {
		t.Fatalf("default merge timeout = %s, %v", value, err)
	}
	config.MergeTimeout = "0s"
	if value, err := config.EffectiveMergeTimeout(); err != nil || value != 0 {
		t.Fatalf("zero merge timeout = %s, %v", value, err)
	}
	for _, invalid := range []string{"-1m", "25h", "soon"} {
		config.MergeTimeout = invalid
		if _, err := config.EffectiveMergeTimeout(); err == nil {
			t.Fatalf("merge_timeout %q accepted", invalid)
		}
	}
}
