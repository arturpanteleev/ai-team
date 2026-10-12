package config

import (
	"strings"
	"testing"
)

func notificationTestConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := DefaultProfile(ProfileStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Notifications = &NotificationsConfig{
		WebBaseURL: "https://ai-team.example",
		Channels: []NotificationChannelConfig{
			{Name: "ops-hook", Type: NotificationWebhook, URLEnv: "AI_TEAM_NOTIFY_WEBHOOK_URL", Events: []string{"stage_stalled"}},
			{Name: "team-slack", Type: NotificationSlack, URLEnv: "AI_TEAM_SLACK_WEBHOOK_URL", Events: []string{"check_overridden", "attempt_finished"}, Functions: []string{"developer", "qa"}},
			{Name: "leads", Type: NotificationEmail, SMTPURLEnv: "AI_TEAM_SMTP_URL", From: "ai-team <bot@example.test>", To: []string{"lead@example.test"}, Events: []string{"integration_error"}},
		},
	}
	return cfg
}

func TestNotificationsConfigValidatesAndRoundTrips(t *testing.T) {
	cfg := notificationTestConfig(t)
	if err := cfg.Validate(nil); err != nil {
		t.Fatalf("valid notifications rejected: %v", err)
	}
	encoded, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ParseYAML(encoded)
	if err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if loaded.Notifications == nil || len(loaded.Notifications.Channels) != 3 ||
		loaded.Notifications.Channels[1].Functions[1] != "qa" || loaded.Notifications.Channels[2].To[0] != "lead@example.test" {
		t.Fatalf("notifications lost on round-trip: %+v", loaded.Notifications)
	}
}

func TestNotificationsConfigRejectsInvalidSubscriptions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*NotificationsConfig)
		want   string
	}{
		{"unknown event type", func(n *NotificationsConfig) { n.Channels[0].Events = []string{"stage_exploded"} }, `неизвестный тип события "stage_exploded"`},
		{"no events", func(n *NotificationsConfig) { n.Channels[0].Events = nil }, "events должен перечислять"},
		{"unknown channel type", func(n *NotificationsConfig) { n.Channels[0].Type = "pager" }, `type "pager" не поддерживается`},
		{"unknown function", func(n *NotificationsConfig) { n.Channels[1].Functions = []string{"astrologer"} }, `функцию "astrologer"`},
		{"duplicate name", func(n *NotificationsConfig) { n.Channels[1].Name = "ops-hook" }, "повторяется"},
		{"bad env name", func(n *NotificationsConfig) { n.Channels[0].URLEnv = "https://hooks.example/secret" }, "url_env должен быть именем"},
		{"email without recipients", func(n *NotificationsConfig) { n.Channels[2].To = nil }, "to должен содержать"},
		{"email header injection", func(n *NotificationsConfig) { n.Channels[2].From = "bot@example.test\r\nBcc: x@example.test" }, "from должен быть"},
		{"smtp on webhook", func(n *NotificationsConfig) { n.Channels[0].SMTPURLEnv = "AI_TEAM_SMTP_URL" }, "только для type: email"},
		{"web base with credentials", func(n *NotificationsConfig) { n.WebBaseURL = "https://u:p@ai-team.example" }, "web_base_url"},
		{"no channels", func(n *NotificationsConfig) { n.Channels = nil }, "хотя бы один канал"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := notificationTestConfig(t)
			tc.mutate(cfg.Notifications)
			err := cfg.Validate(nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestNotificationsConfigRejectsInlineSecrets(t *testing.T) {
	base := "schema_version: 5\ntemplate: x\ntitle: X\nstages: []\nnotifications:\n  channels:\n    - name: hook\n      type: webhook\n"
	for _, field := range []string{"      url: https://hooks.example/secret\n", "      smtp_password: hunter2\n"} {
		if _, err := ParseYAML([]byte(base + field + "      url_env: X\n      events: [stage_stalled]\n")); err == nil {
			t.Fatalf("inline secret field %q should be rejected", strings.TrimSpace(field))
		}
	}
	if _, err := ParseYAML([]byte("schema_version: 5\ntemplate: x\ntitle: X\nstages: []\nnotifications:\n  slack_url: https://hooks.example\n")); err == nil {
		t.Fatal("unknown notifications field should be rejected")
	}
}
