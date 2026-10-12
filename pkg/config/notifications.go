package config

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/notifier"
	"gopkg.in/yaml.v3"
)

// Notification channel types.
const (
	NotificationWebhook = "webhook"
	NotificationSlack   = "slack"
	NotificationEmail   = "email"
)

// NotificationsConfig configures optional outbound task notifications. Channel
// destinations and credentials are referenced by environment variable name
// and are never stored in the project template.
type NotificationsConfig struct {
	WebBaseURL string                      `yaml:"web_base_url,omitempty"`
	Channels   []NotificationChannelConfig `yaml:"channels"`
}

// NotificationChannelConfig is one destination with its subscription.
type NotificationChannelConfig struct {
	Name       string   `yaml:"name"`
	Type       string   `yaml:"type"`
	URLEnv     string   `yaml:"url_env,omitempty"`
	SMTPURLEnv string   `yaml:"smtp_url_env,omitempty"`
	From       string   `yaml:"from,omitempty"`
	To         []string `yaml:"to,omitempty"`
	Events     []string `yaml:"events"`
	Functions  []string `yaml:"functions,omitempty"`
}

var notificationChannelName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (nc *NotificationsConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{"web_base_url": true, "channels": true}, "config: notifications"); err != nil {
		return err
	}
	type plain NotificationsConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*nc = NotificationsConfig(decoded)
	return nil
}

func (cc *NotificationChannelConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"name": true, "type": true, "url_env": true, "smtp_url_env": true,
		"from": true, "to": true, "events": true, "functions": true,
	}, "config: notifications channel"); err != nil {
		return err
	}
	type plain NotificationChannelConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*cc = NotificationChannelConfig(decoded)
	return nil
}

// Validate checks channel types, environment references and subscriptions.
// functions is the set of stage functions declared by the template.
func (nc *NotificationsConfig) Validate(functions map[string]bool) error {
	if nc == nil {
		return nil
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if nc.WebBaseURL != "" {
		parsed, err := url.Parse(strings.TrimSpace(nc.WebBaseURL))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			add("notifications.web_base_url должен быть абсолютным HTTP(S) URL без credentials, query и fragment")
		}
	}
	if len(nc.Channels) == 0 {
		add("notifications.channels должен содержать хотя бы один канал")
	}
	seen := make(map[string]bool, len(nc.Channels))
	for index, channel := range nc.Channels {
		prefix := fmt.Sprintf("notifications.channels[%d]", index)
		if !notificationChannelName.MatchString(channel.Name) {
			add("%s.name должен соответствовать %s", prefix, notificationChannelName.String())
		} else if seen[channel.Name] {
			add("%s.name %q повторяется", prefix, channel.Name)
		}
		seen[channel.Name] = true
		switch channel.Type {
		case NotificationWebhook, NotificationSlack:
			if !envVariableName.MatchString(channel.URLEnv) {
				add("%s.url_env должен быть именем переменной окружения", prefix)
			}
			if channel.SMTPURLEnv != "" || channel.From != "" || len(channel.To) > 0 {
				add("%s: smtp_url_env, from и to допустимы только для type: email", prefix)
			}
		case NotificationEmail:
			if !envVariableName.MatchString(channel.SMTPURLEnv) {
				add("%s.smtp_url_env должен быть именем переменной окружения", prefix)
			}
			if channel.URLEnv != "" {
				add("%s.url_env недопустим для type: email", prefix)
			}
			if !validMailbox(channel.From) {
				add("%s.from должен быть одним e-mail адресом", prefix)
			}
			if len(channel.To) == 0 {
				add("%s.to должен содержать хотя бы одного получателя", prefix)
			}
			for _, recipient := range channel.To {
				if !validMailbox(recipient) {
					add("%s.to содержит некорректный адрес", prefix)
					break
				}
			}
		default:
			add("%s.type %q не поддерживается (webhook, slack, email)", prefix, channel.Type)
		}
		if len(channel.Events) == 0 {
			add("%s.events должен перечислять типы событий", prefix)
		}
		for _, eventType := range channel.Events {
			if !notifier.IsKnownEventType(eventType) {
				add("%s.events содержит неизвестный тип события %q", prefix, eventType)
			}
		}
		for _, function := range channel.Functions {
			if !functions[function] {
				add("%s.functions содержит функцию %q, которой нет в шаблоне (%s)", prefix, function, strings.Join(sortedKeys(functions), ", "))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func validMailbox(value string) bool {
	if strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address != ""
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
