package config

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/checks"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/gatelib"
	"github.com/arturpanteleev/ai-team/pkg/redact"
	"github.com/arturpanteleev/ai-team/pkg/runtime"
	"github.com/arturpanteleev/ai-team/pkg/stageexec"
	"github.com/arturpanteleev/ai-team/pkg/workflow"
	"gopkg.in/yaml.v3"
)

type AgentConfig struct {
	Name    string              `yaml:"name"`
	Model   string              `yaml:"model,omitempty"`
	Effort  string              `yaml:"effort,omitempty"`
	CLI     string              `yaml:"cli,omitempty"`
	Timeout string              `yaml:"timeout,omitempty"`
	Checks  []checks.Definition `yaml:"checks,omitempty"`
	// MCPServers contains only the named servers selected by this stage.
	MCPServers []runtime.MCPServerConfig `yaml:"-"`
}

type Config struct {
	// QuestionStages — этапы шаблона, чей агент умеет задавать вопросы
	// (ask_questions). Заполняется пайплайном из реестра агентов; по нему
	// шаблон добавляет переход blocked → тот же этап с согласованием ответа.
	QuestionStages map[string]bool `yaml:"-" json:"-"`

	// gateLibrary overrides the built-in gate library during
	// ValidateWithLibrary; nil means gatelib.Default().
	gateLibrary *gatelib.Registry

	SchemaVersion int    `yaml:"schema_version"`
	Template      string `yaml:"template"`
	Title         string `yaml:"title"`
	StallAfter    string `yaml:"stall_after,omitempty"`
	// ForbidSelfApproval запрещает участнику подтверждать этап, который он
	// сам брал или сдавал (B-82). По умолчанию выключено.
	ForbidSelfApproval bool                               `yaml:"forbid_self_approval,omitempty"`
	Stages             []TemplateStage                    `yaml:"stages"`
	Returns            []TemplateReturn                   `yaml:"returns,omitempty"`
	MaxVisits          map[string]int                     `yaml:"max_visits,omitempty"`
	Checks             []checks.Definition                `yaml:"checks,omitempty"`
	MCPServers         map[string]runtime.MCPServerConfig `yaml:"mcp_servers,omitempty"`
	// ExecutorAdapters declares trusted external stage executors by name; a
	// stage selects one with its adapter field.
	ExecutorAdapters map[string]stageexec.AdapterSpec `yaml:"executor_adapters,omitempty"`

	// PipelineAgents and Workflow are runtime compatibility projections. They
	// are never serialized; schema v5 stores stage IDs, agent references and
	// return routes in the template fields above.
	PipelineAgents []AgentConfig   `yaml:"-"`
	Workflow       *WorkflowConfig `yaml:"-"`
	CLI            string          `yaml:"cli,omitempty"`
	Model          string          `yaml:"model,omitempty"`
	Effort         string          `yaml:"effort,omitempty"`
	StageTimeout   string          `yaml:"stage_timeout,omitempty"`
	// PreflightTimeout — бюджет одной внешней команды preflight (опрос версии
	// CLI-рантайма, git, gh). Холодный старт CLI-рантайма на порядок дороже
	// прогретого, поэтому бюджет вынесен в конфигурацию: слишком тесный делает
	// работоспособный рантайм неотличимым от отсутствующего.
	PreflightTimeout string `yaml:"preflight_timeout,omitempty"`
	// DeliveryTimeout — бюджет всей post-terminal доставки (commit, push,
	// gh pr view/create). Доставка одобрена человеком и исполняется после
	// терминального finalize, поэтому её нельзя обрывать бюджетом run'а; но
	// её внешние команды сетевые, и без собственного дедлайна зависший git/gh
	// делает контроллер неостанавливаемым (QS-06).
	DeliveryTimeout string               `yaml:"delivery_timeout,omitempty"`
	Containment     *ContainmentConfig   `yaml:"containment,omitempty"`
	TreeHash        *TreeHashConfig      `yaml:"tree_hash,omitempty"`
	Budget          *BudgetConfig        `yaml:"budget,omitempty"`
	AutoPickup      *AutoPickupConfig    `yaml:"auto_pickup,omitempty"`
	Usage           *UsageConfig         `yaml:"usage,omitempty"`
	Tracker         *TrackerConfig       `yaml:"tracker,omitempty"`
	CICD            *CICDConfig          `yaml:"cicd,omitempty"`
	Notifications   *NotificationsConfig `yaml:"notifications,omitempty"`
	Redaction       *RedactionConfig     `yaml:"redaction,omitempty"`
	Retention       *RetentionConfig     `yaml:"retention,omitempty"`
}

// BudgetConfig — лимит времени одного исполнения и суммарное число попыток
// задачи. Время задачи может включать паузы между исполнениями, время ожидания
// человека не расходует max_execution_time.
type BudgetConfig struct {
	MaxExecutionTime string `yaml:"max_execution_time,omitempty"`
	// MaxWallTime is a backwards-compatible alias for MaxExecutionTime.
	MaxWallTime string `yaml:"max_wall_time,omitempty"`
	MaxAttempts int    `yaml:"max_attempts,omitempty"`
	// MaxTaskTokens — бюджет токенов (input + output) одной задачи. Ноль —
	// без лимита. Превышение останавливает задачу перед следующей попыткой
	// агента; продолжить её может только явное решение человека (resume).
	MaxTaskTokens int64 `yaml:"max_task_tokens,omitempty"`
	// MaxDailyTokens — бюджет токенов инстанса на сутки UTC по всем задачам.
	// Ноль — без лимита. Сутки считаются по UTC, а не по локальному поясу.
	MaxDailyTokens int64 `yaml:"max_daily_tokens,omitempty"`
}

// UnmarshalYAML отвергает неизвестные ключи: опечатка в имени лимита иначе
// молча оставила бы задачу без бюджета.
func (bc *BudgetConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"max_execution_time": true, "max_wall_time": true, "max_attempts": true,
		"max_task_tokens": true, "max_daily_tokens": true,
	}, "config: budget"); err != nil {
		return err
	}
	type plain BudgetConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*bc = BudgetConfig(decoded)
	return nil
}

// UnmarshalYAML отвергает неизвестные ключи секции auto_pickup.
func (ac *AutoPickupConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"enabled": true, "max_parallel_tasks": true, "poll_interval": true,
	}, "config: auto_pickup"); err != nil {
		return err
	}
	type plain AutoPickupConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*ac = AutoPickupConfig(decoded)
	return nil
}

// AutoPickupConfig включает автоматический подбор задач из бэклога: агент
// берёт только элементы, явно помеченные человеком как готовые к автоподбору,
// и запускает их в режиме auto. Включение требует обоих токен-бюджетов —
// подбор без верхней границы расхода не запускается (fail-closed).
type AutoPickupConfig struct {
	Enabled bool `yaml:"enabled"`
	// MaxParallelTasks — лимит незавершённых задач на инстанс. Задачи в
	// ожидании человека тоже занимают слот: незавершённая задача — это работа,
	// за которую кто-то отвечает.
	MaxParallelTasks int `yaml:"max_parallel_tasks,omitempty"`
	// PollInterval — как часто подборщик проверяет очередь.
	PollInterval string `yaml:"poll_interval,omitempty"`
}

// Канонические дефолты автоподбора. Один параллельный слот совпадает с
// workspace lock локального контроллера: два run одного target одновременно
// он всё равно не исполнит.
const (
	DefaultAutoPickupMaxParallelTasks = 1
	DefaultAutoPickupPollInterval     = time.Minute
)

// EffectiveMaxParallelTasks возвращает лимит параллельных задач: явный или дефолт.
func (ac *AutoPickupConfig) EffectiveMaxParallelTasks() int {
	if ac == nil || ac.MaxParallelTasks <= 0 {
		return DefaultAutoPickupMaxParallelTasks
	}
	return ac.MaxParallelTasks
}

// EffectivePollInterval возвращает период опроса очереди: явный или дефолт.
func (ac *AutoPickupConfig) EffectivePollInterval() time.Duration {
	if ac == nil || strings.TrimSpace(ac.PollInterval) == "" {
		return DefaultAutoPickupPollInterval
	}
	duration, err := time.ParseDuration(ac.PollInterval)
	if err != nil || duration <= 0 {
		return DefaultAutoPickupPollInterval
	}
	return duration
}

// Validate проверяет автоподбор вместе с бюджетом: включённый подбор без
// обоих токен-лимитов отвергается.
func (ac *AutoPickupConfig) Validate(budget *BudgetConfig) error {
	if ac == nil {
		return nil
	}
	if ac.MaxParallelTasks < 0 {
		return fmt.Errorf("auto_pickup.max_parallel_tasks не может быть отрицательным")
	}
	if ac.PollInterval != "" {
		duration, err := time.ParseDuration(ac.PollInterval)
		if err != nil || duration <= 0 {
			return fmt.Errorf("auto_pickup.poll_interval %q не парсится (пример: 1m)", ac.PollInterval)
		}
	}
	if ac.Enabled && (budget == nil || budget.MaxTaskTokens <= 0 || budget.MaxDailyTokens <= 0) {
		return fmt.Errorf("auto_pickup.enabled требует budget.max_task_tokens и budget.max_daily_tokens больше нуля")
	}
	return nil
}

// UsageConfig enables an explicitly approximate allocation of a monthly
// subscription across recorded runs (never an API price estimate) and,
// separately, a per-model provider tariff table (B-66). The tariff table is
// empty by default: ai-team ships no prices, the project records the
// provider's published prices together with their date and source.
type UsageConfig struct {
	MonthlySubscriptionAmount   float64        `yaml:"monthly_subscription_amount,omitempty"`
	MonthlySubscriptionCurrency string         `yaml:"monthly_subscription_currency,omitempty"`
	Tariffs                     []TariffConfig `yaml:"tariffs,omitempty"`
}

// TariffConfig is one provider price list entry for a model, in currency
// units per one million tokens. EffectiveDate (YYYY-MM-DD, UTC) is the date
// the prices were taken from the provider and from which they apply: an
// attempt is priced by the entry for its model with the latest effective
// date not after the attempt. CachedInputPerMTok and CacheWritePerMTok are
// optional; an attempt with cache tokens and no matching price is unpriced.
type TariffConfig struct {
	Model              string   `yaml:"model"`
	InputPerMTok       *float64 `yaml:"input_per_mtok"`
	OutputPerMTok      *float64 `yaml:"output_per_mtok"`
	CachedInputPerMTok *float64 `yaml:"cached_input_per_mtok,omitempty"`
	CacheWritePerMTok  *float64 `yaml:"cache_write_per_mtok,omitempty"`
	Currency           string   `yaml:"currency"`
	EffectiveDate      string   `yaml:"effective_date"`
	Source             string   `yaml:"source"`
}

// TrackerConfig enables optional one-way integrations with external trackers.
// Credentials are referenced by environment variable name and never stored in
// the project template.
type TrackerConfig struct {
	Jira *JiraConfig `yaml:"jira,omitempty"`
}

// JiraConfig configures Jira Cloud synchronization for completed stages.
type JiraConfig struct {
	BaseURL       string            `yaml:"base_url"`
	Email         string            `yaml:"email"`
	TokenEnv      string            `yaml:"token_env"`
	WebBaseURL    string            `yaml:"web_base_url"`
	StageStatuses map[string]string `yaml:"stage_statuses,omitempty"`
}

func (tc *TrackerConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{"jira": true}, "config: tracker"); err != nil {
		return err
	}
	type plain TrackerConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*tc = TrackerConfig(decoded)
	return nil
}

func (jc *JiraConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"base_url": true, "email": true, "token_env": true, "web_base_url": true,
		"stage_statuses": true,
	}, "config: tracker jira"); err != nil {
		return err
	}
	for index := 0; index < len(value.Content); index += 2 {
		if value.Content[index].Value == "stage_statuses" {
			if err := rejectDuplicateMappingKeys(value.Content[index+1], "config: tracker jira stage_statuses"); err != nil {
				return err
			}
		}
	}
	type plain JiraConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*jc = JiraConfig(decoded)
	return nil
}

func (tc *TrackerConfig) Validate(stageIDs map[string]bool) error {
	if tc == nil {
		return nil
	}
	if tc.Jira == nil {
		return fmt.Errorf("tracker must configure jira")
	}
	return tc.Jira.Validate(stageIDs)
}

func (jc *JiraConfig) Validate(stageIDs map[string]bool) error {
	if jc == nil {
		return nil
	}
	var problems []string
	for name, value := range map[string]string{
		"base_url": jc.BaseURL, "web_base_url": jc.WebBaseURL,
	} {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			problems = append(problems, fmt.Sprintf("tracker.jira.%s должен быть абсолютным HTTP(S) URL без credentials, query и fragment", name))
		}
		if name == "base_url" && err == nil && parsed != nil && parsed.Scheme != "https" {
			problems = append(problems, "tracker.jira.base_url должен использовать HTTPS для защиты API token")
		}
	}
	if strings.TrimSpace(jc.Email) == "" || strings.ContainsAny(jc.Email, "\r\n\x00") {
		problems = append(problems, "tracker.jira.email обязателен и не должен содержать управляющие символы")
	}
	if !envVariableName.MatchString(jc.TokenEnv) {
		problems = append(problems, "tracker.jira.token_env должен быть именем переменной окружения")
	}
	for stage, status := range jc.StageStatuses {
		if !stageIDs[stage] {
			problems = append(problems, fmt.Sprintf("tracker.jira.stage_statuses содержит неизвестный этап %q", stage))
		}
		if strings.TrimSpace(status) == "" || strings.ContainsAny(status, "\r\n\x00") {
			problems = append(problems, fmt.Sprintf("tracker.jira.stage_statuses.%s должен содержать имя статуса Jira", stage))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

var envVariableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (uc *UsageConfig) Validate() error {
	if uc == nil {
		return nil
	}
	amount := uc.MonthlySubscriptionAmount
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return fmt.Errorf("usage.monthly_subscription_amount должен быть конечным неотрицательным числом")
	}
	currency := uc.MonthlySubscriptionCurrency
	if currency != "" && !isCurrencyCode(currency) {
		return fmt.Errorf("usage.monthly_subscription_currency должен быть ISO-кодом из трёх заглавных букв")
	}
	seen := map[string]bool{}
	for index, tariff := range uc.Tariffs {
		if err := tariff.validate(index); err != nil {
			return err
		}
		key := tariff.Model + "\x00" + tariff.EffectiveDate
		if seen[key] {
			return fmt.Errorf("usage.tariffs[%d]: тариф модели %q на дату %s задан дважды", index, tariff.Model, tariff.EffectiveDate)
		}
		seen[key] = true
	}
	return nil
}

func isCurrencyCode(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, char := range currency {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func (tc TariffConfig) validate(index int) error {
	prefix := fmt.Sprintf("usage.tariffs[%d]", index)
	if strings.TrimSpace(tc.Model) == "" || tc.Model != strings.TrimSpace(tc.Model) || len(tc.Model) > 200 || strings.ContainsAny(tc.Model, "\r\n\x00") {
		return fmt.Errorf("%s.model обязателен: имя модели, как оно задано в model агента", prefix)
	}
	for _, field := range []struct {
		name     string
		price    *float64
		required bool
	}{
		{"input_per_mtok", tc.InputPerMTok, true}, {"output_per_mtok", tc.OutputPerMTok, true},
		{"cached_input_per_mtok", tc.CachedInputPerMTok, false}, {"cache_write_per_mtok", tc.CacheWritePerMTok, false},
	} {
		name, price, required := field.name, field.price, field.required
		if price == nil {
			if required {
				return fmt.Errorf("%s.%s обязателен", prefix, name)
			}
			continue
		}
		if math.IsNaN(*price) || math.IsInf(*price, 0) || *price < 0 {
			return fmt.Errorf("%s.%s должен быть конечным неотрицательным числом", prefix, name)
		}
	}
	if !isCurrencyCode(tc.Currency) {
		return fmt.Errorf("%s.currency обязателен: ISO-код из трёх заглавных букв", prefix)
	}
	if _, err := time.Parse("2006-01-02", tc.EffectiveDate); err != nil {
		return fmt.Errorf("%s.effective_date обязателен в формате YYYY-MM-DD", prefix)
	}
	if strings.TrimSpace(tc.Source) == "" || strings.ContainsAny(tc.Source, "\r\n\x00") {
		return fmt.Errorf("%s.source обязателен: откуда взяты цены (ссылка на прайс провайдера)", prefix)
	}
	return nil
}

// Канонические дефолты бюджета (применяются при отсутствии явного budget).
const (
	DefaultBudgetMaxWallTime = "24h"
	DefaultBudgetMaxAttempts = 100
)

// DefaultBudgetMaxWallTimeDuration is the default budget for one execution
// invocation. A paused task gets a fresh execution budget when it resumes;
// its overall elapsed time remains measured from original creation.
const DefaultBudgetMaxWallTimeDuration = 24 * time.Hour

// DefaultStageTimeout — бюджет одной стадии по умолчанию. То же значение,
// которое `ai-team init` пишет в сгенерированный конфиг (stage_timeout: 30m);
// здесь оно продублировано на уровне кода, потому что конфиг, написанный
// руками, этого поля может не содержать, а стадия без таймаута — это зависший
// CLI-рантайм без верхней границы.
const DefaultStageTimeout = 30 * time.Minute

// DefaultPreflightTimeout — бюджет одной внешней команды preflight по
// умолчанию. Замер холодного старта opencode (рантайм по умолчанию) — 2.76 с
// против 0.29 с прогретого; 60 с дают двадцатикратный запас и при этом не
// превращают сломанный рантайм в долгое ожидание без диагностики.
const DefaultPreflightTimeout = 60 * time.Second

// EffectivePreflightTimeout возвращает бюджет внешней команды preflight.
// Пустое или непарсящееся значение даёт канонический дефолт: валидация конфига
// уже отвергает некорректные значения, поэтому здесь fallback, а не ошибка.
func (c *Config) EffectivePreflightTimeout() time.Duration {
	if c == nil || strings.TrimSpace(c.PreflightTimeout) == "" {
		return DefaultPreflightTimeout
	}
	duration, err := time.ParseDuration(c.PreflightTimeout)
	if err != nil || duration <= 0 {
		return DefaultPreflightTimeout
	}
	return duration
}

// DefaultDeliveryTimeout — бюджет всей доставки по умолчанию. Замер доставки
// до живого remote — 2.90 с; 10 минут дают двухсоткратный запас (медленная
// сеть, большой push, холодный `gh auth`) и при этом превращают зависший
// git/gh из неостанавливаемого ожидания в ошибку с внятной причиной.
// Отдельный бюджет на команду не вводится намеренно: шагов около двадцати,
// а человеку важен предсказуемый потолок операции целиком.
const DefaultDeliveryTimeout = 10 * time.Minute

// EffectiveDeliveryTimeout возвращает бюджет доставки. Пустое или
// непарсящееся значение даёт канонический дефолт: валидация конфига уже
// отвергает некорректные значения, поэтому здесь fallback, а не ошибка.
// Ноль здесь невозможен by design — доставка без дедлайна и есть дефект QS-06.
func (c *Config) EffectiveDeliveryTimeout() time.Duration {
	if c == nil || strings.TrimSpace(c.DeliveryTimeout) == "" {
		return DefaultDeliveryTimeout
	}
	duration, err := time.ParseDuration(c.DeliveryTimeout)
	if err != nil || duration <= 0 {
		return DefaultDeliveryTimeout
	}
	return duration
}

// EffectiveMaxWallTime возвращает бюджет одного исполнения и его метку.
// EffectiveMaxExecutionTime is the preferred name; this wrapper remains for
// source compatibility with existing callers.
func (bc *BudgetConfig) EffectiveMaxWallTime() (time.Duration, string) {
	return bc.EffectiveMaxExecutionTime()
}

// EffectiveMaxExecutionTime returns the budget for one execution invocation.
// Nona-positive durations never escape this helper: the caller always arms a
// deadline, including when no budget section was configured.
//
// Непарсящееся или неположительное значение тоже даёт дефолт, а не ноль:
// Validate отвергает такие конфиги до запуска, так что сюда они попадают
// только программно, и падать в «без лимита» здесь опаснее, чем подставить
// канонический. Метка при этом возвращается дефолтная.
func (bc *BudgetConfig) EffectiveMaxExecutionTime() (time.Duration, string) {
	if bc == nil || (strings.TrimSpace(bc.MaxExecutionTime) == "" && strings.TrimSpace(bc.MaxWallTime) == "") {
		return DefaultBudgetMaxWallTimeDuration, DefaultBudgetMaxWallTime
	}
	value := bc.MaxExecutionTime
	if strings.TrimSpace(value) == "" {
		value = bc.MaxWallTime
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return DefaultBudgetMaxWallTimeDuration, DefaultBudgetMaxWallTime
	}
	return d, value
}

// EffectiveMaxAttempts возвращает лимит попыток: явный или дефолт.
func (bc *BudgetConfig) EffectiveMaxAttempts() int {
	if bc == nil || bc.MaxAttempts <= 0 {
		return DefaultBudgetMaxAttempts
	}
	return bc.MaxAttempts
}

func (bc *BudgetConfig) Validate() error {
	if bc == nil {
		return nil
	}
	if bc.MaxExecutionTime != "" && bc.MaxWallTime != "" {
		return fmt.Errorf("budget.max_execution_time и устаревший max_wall_time нельзя задавать вместе")
	}
	for name, value := range map[string]string{"max_execution_time": bc.MaxExecutionTime, "max_wall_time": bc.MaxWallTime} {
		if value == "" {
			continue
		}
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("budget.%s %q не парсится (пример: 2h)", name, value)
		}
	}
	if bc.MaxAttempts < 0 {
		return fmt.Errorf("budget.max_attempts не может быть отрицательным")
	}
	if bc.MaxTaskTokens < 0 {
		return fmt.Errorf("budget.max_task_tokens не может быть отрицательным")
	}
	if bc.MaxDailyTokens < 0 {
		return fmt.Errorf("budget.max_daily_tokens не может быть отрицательным")
	}
	return nil
}

// RedactionConfig — privacy-контракт (P1-6): полевая классификация, secrets
// scanner, include/exclude policy и fail-closed блок экспорта. raw_logs —
// opt-in сохранение raw вывода; без явного true raw logs не считаются
// разрешёнными. Блок экспорта по секретам включён ПО УМОЛЧАНИЮ (fail-closed):
// отключить можно только явным disable_export_block: true. Никаких обещаний
// legal compliance.
//
// ScanSensitiveFields и RawLogs — зарезервированные future knobs: сейчас
// `scan`/`verify` работают по ключам событий через ClassifyField, а raw-вывод
// никогда не пишется в evidence. Поля принимаются конфигом без ошибки, но
// эффект появится вместе с соответствующими фичами; менять семантику по
// умолчанию они не должны.
type RedactionConfig struct {
	ScanSensitiveFields bool     `yaml:"scan_sensitive_fields,omitempty"`
	Include             []string `yaml:"include,omitempty"`
	Exclude             []string `yaml:"exclude,omitempty"`
	RawLogs             bool     `yaml:"raw_logs,omitempty"`
	DisableExportBlock  bool     `yaml:"disable_export_block,omitempty"`
}

func (rc *RedactionConfig) Validate() error {
	if rc == nil {
		return nil
	}
	policy := redact.Policy{Include: rc.Include, Exclude: rc.Exclude}
	return policy.Validate()
}

// EffectiveFailExportOnSecrets — эффект fail-closed блока экспорта
// (включён по умолчанию; отключается только явным disable_export_block).
func (rc *RedactionConfig) EffectiveFailExportOnSecrets() bool {
	if rc == nil {
		return true
	}
	return !rc.DisableExportBlock
}

// RetentionConfig — настраиваемая retention (P1-6): дефолты для `ai-team gc`
// без обещания legal compliance (это техническая уборка, не obligations-контракт).
type RetentionConfig struct {
	OlderThan string `yaml:"older_than,omitempty"`
	KeepLast  int    `yaml:"keep_last,omitempty"`
	PruneRuns bool   `yaml:"prune_runs,omitempty"`
}

// Канонические дефолты retention.
const (
	DefaultRetentionOlderThan = "720h"
	DefaultRetentionKeepLast  = 20
)

func (rc *RetentionConfig) Validate() error {
	if rc == nil {
		return nil
	}
	if rc.OlderThan != "" {
		if _, err := time.ParseDuration(rc.OlderThan); err != nil {
			return fmt.Errorf("retention.older_than %q не парсится (пример: 720h)", rc.OlderThan)
		}
	}
	if rc.KeepLast < 0 {
		return fmt.Errorf("retention.keep_last не может быть отрицательным")
	}
	return nil
}

// EffectiveOlderThanDuration — распарсенный older_than (0 — канонический
// дефолт 720h).
func (rc *RetentionConfig) EffectiveOlderThanDuration() (time.Duration, error) {
	if rc == nil || rc.OlderThan == "" {
		d, err := time.ParseDuration(DefaultRetentionOlderThan)
		if err != nil {
			return 0, err
		}
		return d, nil
	}
	return time.ParseDuration(rc.OlderThan)
}

// EffectiveKeepLast возвращает keep_last или дефолт.
func (rc *RetentionConfig) EffectiveKeepLast() int {
	if rc == nil || rc.KeepLast <= 0 {
		return DefaultRetentionKeepLast
	}
	return rc.KeepLast
}

// TreeHashConfig — project-specific настройки tree hashing (OPS-2).
// IgnoreDirs добавляет имена каталогов к каноническому baseline без его
// ослабления. Только простые имена (строгий ValidIgnoreDirName), никаких путей
// или glob'ов.
type TreeHashConfig struct {
	IgnoreDirs []string `yaml:"ignore_dirs,omitempty"`
}

// ValidIgnoreDirName — строгий шаблон имени каталога для tree-hash ignore
// (канонический — в pkg/checks, где живёт tree hashing).
func ValidIgnoreDirName(name string) bool { return checks.ValidIgnoreDirName(name) }

func (tc *TreeHashConfig) Validate() error {
	if tc == nil {
		return nil
	}
	seen := make(map[string]bool, len(tc.IgnoreDirs))
	for _, name := range tc.IgnoreDirs {
		if !checks.ValidIgnoreDirName(name) {
			return fmt.Errorf("tree_hash.ignore_dirs: недопустимое имя каталога %q (допустимо только простое имя: буквы/цифры/-/./_, без '/', не '.'/'..', без ведущего '-')", name)
		}
		if seen[name] {
			return fmt.Errorf("tree_hash.ignore_dirs: дубликат %q", name)
		}
		seen[name] = true
	}
	return nil
}

type ContainmentConfig struct {
	Profile string `yaml:"profile"`
}

var validContainmentProfiles = map[string]bool{
	"trusted-local": true, "strict": true,
}

func (cc *ContainmentConfig) Validate() error {
	if cc == nil {
		return nil
	}
	if cc.Profile == "" {
		cc.Profile = "trusted-local"
	}
	if !validContainmentProfiles[cc.Profile] {
		return fmt.Errorf("containment.profile: неизвестный профиль %q (допустимы: trusted-local, strict)", cc.Profile)
	}
	return nil
}

type WorkflowConfig struct {
	Entry     string               `yaml:"entry"`
	MaxVisits map[string]int       `yaml:"max_visits,omitempty"`
	Edges     []WorkflowEdgeConfig `yaml:"edges"`
}

type WorkflowEdgeConfig struct {
	From     string                  `yaml:"from"`
	Outcome  string                  `yaml:"outcome"`
	To       string                  `yaml:"to"`
	Approval *WorkflowApprovalConfig `yaml:"approval,omitempty"`
}

type WorkflowApprovalConfig struct {
	Roles   []string          `yaml:"roles"`
	Quorum  string            `yaml:"quorum"`
	Actions map[string]string `yaml:"actions"`
	// Deferred — см. workflow.ApprovalPolicy.Deferred.
	Deferred bool `yaml:"deferred,omitempty"`
}

func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("config: expected mapping")
	}
	version := 0
	for index := 0; index < len(value.Content); index += 2 {
		if value.Content[index].Value == "schema_version" {
			if err := value.Content[index+1].Decode(&version); err != nil {
				return fmt.Errorf("config: schema_version должен быть целым числом: %w", err)
			}
			break
		}
	}
	if version == 4 {
		return fmt.Errorf("config: схема v4 больше не поддерживается; выполните `ai-team init --force` или перенесите конфиг вручную по docs/reference/config.md")
	}
	if !supportedSchemaVersion(version) {
		if version == 0 {
			return fmt.Errorf("config: schema_version обязателен (поддерживаются %d и %d)", LegacySchemaVersion, CurrentSchemaVersion)
		}
		return fmt.Errorf("config: schema_version %d не поддерживается (поддерживаются %d и %d); выполните `ai-team init --force` или перенесите конфиг вручную по docs/reference/config.md", version, LegacySchemaVersion, CurrentSchemaVersion)
	}
	if version == CurrentSchemaVersion {
		for index := 0; index+1 < len(value.Content); index += 2 {
			if value.Content[index].Value == "stages" {
				if err := rejectLegacyGateKeys(value.Content[index+1]); err != nil {
					return err
				}
			}
		}
	}
	if err := validateMappingKeys(value, map[string]bool{
		"schema_version": true, "template": true, "title": true,
		"stall_after": true, "forbid_self_approval": true, "stages": true, "returns": true,
		"max_visits": true, "checks": true, "cli": true, "model": true,
		"effort": true, "stage_timeout": true, "preflight_timeout": true,
		"delivery_timeout": true, "containment": true, "usage": true,
		"tree_hash": true, "budget": true, "auto_pickup": true, "tracker": true, "cicd": true, "notifications": true, "redaction": true, "retention": true,
		"mcp_servers": true, "executor_adapters": true,
	}, "config"); err != nil {
		return err
	}
	type rawConfig struct {
		SchemaVersion      int                                `yaml:"schema_version"`
		Template           string                             `yaml:"template"`
		Title              string                             `yaml:"title"`
		StallAfter         string                             `yaml:"stall_after"`
		ForbidSelfApproval bool                               `yaml:"forbid_self_approval"`
		Stages             []TemplateStage                    `yaml:"stages"`
		Returns            []TemplateReturn                   `yaml:"returns"`
		MaxVisits          map[string]int                     `yaml:"max_visits"`
		Checks             []checks.Definition                `yaml:"checks"`
		MCPServers         map[string]runtime.MCPServerConfig `yaml:"mcp_servers"`
		ExecutorAdapters   map[string]stageexec.AdapterSpec   `yaml:"executor_adapters"`
		CLI                string                             `yaml:"cli"`
		Model              string                             `yaml:"model"`
		Effort             string                             `yaml:"effort"`
		StageTimeout       string                             `yaml:"stage_timeout"`
		PreflightTimeout   string                             `yaml:"preflight_timeout"`
		DeliveryTimeout    string                             `yaml:"delivery_timeout"`
		Containment        *ContainmentConfig                 `yaml:"containment"`
		Usage              *UsageConfig                       `yaml:"usage"`
		Tracker            *TrackerConfig                     `yaml:"tracker"`
		CICD               *CICDConfig                        `yaml:"cicd"`
		Notifications      *NotificationsConfig               `yaml:"notifications"`
		TreeHash           *TreeHashConfig                    `yaml:"tree_hash"`
		Budget             *BudgetConfig                      `yaml:"budget"`
		AutoPickup         *AutoPickupConfig                  `yaml:"auto_pickup"`
		Redaction          *RedactionConfig                   `yaml:"redaction"`
		Retention          *RetentionConfig                   `yaml:"retention"`
	}
	var raw rawConfig
	if err := value.Decode(&raw); err != nil {
		return err
	}

	c.SchemaVersion = raw.SchemaVersion
	c.Template = raw.Template
	c.Title = raw.Title
	c.StallAfter = raw.StallAfter
	c.ForbidSelfApproval = raw.ForbidSelfApproval
	c.Stages = raw.Stages
	for index := range c.Stages {
		switch {
		case len(c.Stages[index].ExitGateGroups) > 0 && version == LegacySchemaVersion:
			return fmt.Errorf("config: stages[%d] (%s): exit_gate_groups доступен со schema_version %d; в v5 используйте required_sections, check, confirm", index, c.Stages[index].ID, CurrentSchemaVersion)
		case version == CurrentSchemaVersion:
			// v6: the gate chain is the single source; the absence of a human
			// gate means no confirmation (v5's per-result default is gone).
			c.Stages[index].applyGateProjection()
		}
	}
	c.Returns = raw.Returns
	c.MaxVisits = raw.MaxVisits
	c.Checks = raw.Checks
	c.MCPServers = raw.MCPServers
	c.ExecutorAdapters = raw.ExecutorAdapters
	c.CLI = raw.CLI
	c.Model = raw.Model
	c.Effort = raw.Effort
	c.StageTimeout = raw.StageTimeout
	c.PreflightTimeout = raw.PreflightTimeout
	c.DeliveryTimeout = raw.DeliveryTimeout
	c.Containment = raw.Containment
	c.TreeHash = raw.TreeHash
	c.Budget = raw.Budget
	c.AutoPickup = raw.AutoPickup
	c.Usage = raw.Usage
	c.Tracker = raw.Tracker
	c.CICD = raw.CICD
	c.Notifications = raw.Notifications
	c.Redaction = raw.Redaction
	c.Retention = raw.Retention

	// Internal compatibility view used by the current execution API. Stage IDs
	// remain keys; the referenced registry agent is carried by the workflow node.
	c.PipelineAgents = make([]AgentConfig, 0, len(c.Stages))
	for _, stage := range c.Stages {
		c.PipelineAgents = append(c.PipelineAgents, AgentConfig{Name: stage.ID})
	}

	return nil
}

func (w *WorkflowConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"entry": true, "max_visits": true, "edges": true,
	}, "config: workflow"); err != nil {
		return err
	}
	for index := 0; index < len(value.Content); index += 2 {
		if value.Content[index].Value == "max_visits" {
			if err := rejectDuplicateMappingKeys(value.Content[index+1], "config: workflow max_visits"); err != nil {
				return err
			}
		}
	}
	type plain WorkflowConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*w = WorkflowConfig(decoded)
	return nil
}

func (e *WorkflowEdgeConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"from": true, "outcome": true, "to": true, "approval": true,
	}, "config: workflow edge"); err != nil {
		return err
	}
	type plain WorkflowEdgeConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*e = WorkflowEdgeConfig(decoded)
	return nil
}

func (a *WorkflowApprovalConfig) UnmarshalYAML(value *yaml.Node) error {
	if err := validateMappingKeys(value, map[string]bool{
		"roles": true, "quorum": true, "actions": true, "deferred": true,
	}, "config: workflow approval"); err != nil {
		return err
	}
	for index := 0; index < len(value.Content); index += 2 {
		if value.Content[index].Value == "actions" {
			if err := rejectDuplicateMappingKeys(value.Content[index+1], "config: workflow approval actions"); err != nil {
				return err
			}
		}
	}
	type plain WorkflowApprovalConfig
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*a = WorkflowApprovalConfig(decoded)
	return nil
}

func rejectDuplicateMappingKeys(node *yaml.Node, context string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected mapping", context)
	}
	seen := map[string]bool{}
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index].Value
		if seen[key] {
			return fmt.Errorf("%s: duplicate field %q", context, key)
		}
		seen[key] = true
	}
	return nil
}

func validateMappingKeys(node *yaml.Node, allowed map[string]bool, context string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected mapping", context)
	}
	seen := make(map[string]bool)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if seen[key] {
			return fmt.Errorf("%s: duplicate field %q", context, key)
		}
		seen[key] = true
		if !allowed[key] {
			return fmt.Errorf("%s: unknown field %q", context, key)
		}
	}
	return nil
}

func (c *Config) AgentNames() []string {
	if c.Template != "" {
		return c.templateAgentNames()
	}
	names := make([]string, len(c.PipelineAgents))
	for i, a := range c.PipelineAgents {
		names[i] = a.Name
	}
	return names
}

// RegistryAgentName resolves a stable v5 stage ID to the agent definition that
// executes it. Legacy graph nodes are already registry names, so they fall
// through unchanged.
func (c *Config) RegistryAgentName(stageID string) string {
	if c != nil && c.Template != "" {
		for _, stage := range c.Stages {
			if stage.ID == stageID && stage.Agent != "" {
				return stage.Agent
			}
		}
	}
	return stageID
}

// CompiledGraph compiles a v5 template or an in-memory legacy workflow into
// the immutable graph contract used by the current runtime.
func (c *Config) CompiledGraph() (workflow.Graph, error) {
	if c.Template != "" || len(c.Stages) > 0 {
		return c.TemplateGraph()
	}
	graph := workflow.Graph{SchemaVersion: GraphSchemaVersion}
	for _, stage := range c.PipelineAgents {
		graph.Nodes = append(graph.Nodes, workflow.Node{Name: stage.Name})
	}
	if len(graph.Nodes) == 0 {
		return graph, fmt.Errorf("workflow graph: pipeline пуст")
	}
	if c.Workflow == nil {
		return graph, fmt.Errorf("config: in-memory legacy graph requires workflow")
	}
	graph.Entry = c.Workflow.Entry
	knownNodes := make(map[string]bool, len(graph.Nodes))
	for _, node := range graph.Nodes {
		knownNodes[node.Name] = true
	}
	for name := range c.Workflow.MaxVisits {
		if !knownNodes[name] {
			return graph, fmt.Errorf("workflow graph: max_visits ссылается на неизвестный node %q", name)
		}
	}
	for index := range graph.Nodes {
		graph.Nodes[index].MaxVisits = c.Workflow.MaxVisits[graph.Nodes[index].Name]
	}
	for _, edge := range c.Workflow.Edges {
		compiled := workflow.Edge{
			From: edge.From, Outcome: workflow.Outcome(edge.Outcome), To: edge.To,
		}
		if edge.Approval != nil {
			compiled.Approval = &workflow.ApprovalPolicy{
				Roles:    append([]string(nil), edge.Approval.Roles...),
				Quorum:   edge.Approval.Quorum,
				Actions:  copyTargets(edge.Approval.Actions),
				Deferred: edge.Approval.Deferred,
			}
		}
		graph.Edges = append(graph.Edges, compiled)
	}
	if err := graph.Validate(true, true); err != nil {
		return graph, err
	}
	return graph, nil
}

func copyTargets(source map[string]string) map[string]string {
	targets := make(map[string]string, len(source))
	for action, target := range source {
		targets[action] = target
	}
	return targets
}

// AgentConfig возвращает конфигурацию агента с подставленными глобальными
// значениями и дефолтами.
func (c *Config) AgentConfig(name string) *AgentConfig {
	var resolved *AgentConfig
	for _, a := range c.PipelineAgents {
		if a.Name == name {
			cfg := a
			if cfg.Model == "" {
				cfg.Model = c.Model
			}
			if cfg.Effort == "" {
				cfg.Effort = c.Effort
			}
			if cfg.CLI == "" {
				cfg.CLI = c.CLI
			}
			if cfg.Timeout == "" {
				cfg.Timeout = c.StageTimeout
			}
			resolved = &cfg
			break
		}
	}
	if c.Template != "" {
		for _, stage := range c.Stages {
			if stage.Delivery != nil && (stage.ID == name || stage.Agent == name) {
				if resolved == nil {
					resolved = &AgentConfig{Name: name}
				}
				for _, requiredName := range stage.Delivery.RequireChecks {
					for _, definition := range c.Checks {
						if definition.Name == requiredName {
							resolved.Checks = append(resolved.Checks, definition)
							break
						}
					}
				}
			}
			if stage.ID != name {
				continue
			}
			for _, serverID := range stage.MCPServers {
				if server, ok := c.MCPServers[serverID]; ok {
					if resolved == nil {
						resolved = &AgentConfig{Name: name}
					}
					server.Name = serverID
					resolved.MCPServers = append(resolved.MCPServers, server)
				}
			}
		}
	}
	return resolved
}

// StageTimeoutFor возвращает таймаут этапа: явный (per-agent `timeout` или
// унаследованный глобальный `stage_timeout`) либо DefaultStageTimeout.
// Результат всегда положителен: «без таймаута» как состояние убрано — конфиг,
// написанный руками без stage_timeout, иначе шёл бы и без бюджета стадии, и
// (до QS-09) без бюджета run'а, то есть без верхней границы вообще.
//
// Ошибка сохранена для непарсящегося значения: Validate отвергает такие
// конфиги до запуска, и молча подменять написанное пользователем число другим
// бюджетом — хуже, чем остановиться с явной диагностикой.
func (ac *AgentConfig) StageTimeoutFor() (time.Duration, error) {
	if ac == nil || strings.TrimSpace(ac.Timeout) == "" {
		return DefaultStageTimeout, nil
	}
	duration, err := time.ParseDuration(ac.Timeout)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("timeout %q должен быть положительным", ac.Timeout)
	}
	return duration, nil
}

// AgentLookup отвечает, существует ли агент (реализуется registry).
type AgentLookup interface {
	Exists(name string) bool
}

type pipelineValidator interface {
	ValidatePipeline(names []string) error
}

// productSpecContractLookup exposes the analyst artifact contract to config
// validation without coupling config to the agent registry implementation.
type productSpecContractLookup interface {
	HasProductSpecContract(name string) (bool, error)
}

// requiredVerdictContractLookup lets template validation verify that an
// explicitly required earlier stage has an immutable verdict contract.
type requiredVerdictContractLookup interface {
	HasRequiredVerdictContract(name string) (bool, error)
}

// Validate проверяет конфиг до запуска пайплайна (fail fast).
func (c *Config) Validate(reg AgentLookup) error {
	if c.Template == "" && len(c.Stages) > 0 {
		return fmt.Errorf("config: template обязателен, если указаны stages")
	}
	if c.Template == "" && len(c.PipelineAgents) == 0 {
		return fmt.Errorf("config: pipeline пуст")
	}
	var errs []string
	validate := func(cond bool, format string, args ...interface{}) {
		if !cond {
			errs = append(errs, fmt.Sprintf(format, args...))
		}
	}
	if !supportedSchemaVersion(c.SchemaVersion) {
		if c.SchemaVersion == 4 {
			addMigration := "схема v4 больше не поддерживается; выполните `ai-team init --force` или перенесите конфиг вручную по docs/reference/config.md"
			errs = append(errs, addMigration)
		} else {
			validate(false,
				"schema_version %d не поддерживается (поддерживаются %d и %d)",
				c.SchemaVersion, LegacySchemaVersion, CurrentSchemaVersion)
		}
	}

	if c.StageTimeout != "" {
		if duration, err := time.ParseDuration(c.StageTimeout); err != nil || duration <= 0 {
			errs = append(errs, fmt.Sprintf("stage_timeout %q не парсится (пример: 30m)", c.StageTimeout))
		}
	}
	if c.PreflightTimeout != "" {
		if duration, err := time.ParseDuration(c.PreflightTimeout); err != nil || duration <= 0 {
			errs = append(errs, fmt.Sprintf("preflight_timeout %q не парсится (пример: 60s)", c.PreflightTimeout))
		}
	}
	if c.DeliveryTimeout != "" {
		if duration, err := time.ParseDuration(c.DeliveryTimeout); err != nil || duration <= 0 {
			errs = append(errs, fmt.Sprintf("delivery_timeout %q не парсится (пример: 10m)", c.DeliveryTimeout))
		}
	}
	validate(isOneOf(c.Effort, "", "low", "medium", "high"),
		"effort %q недопустим (low|medium|high)", c.Effort)
	validate(c.CLI == "" || runtime.AdapterExists(c.CLI),
		"cli %q не поддерживается (неизвестный runtime adapter; доступны: %s)", c.CLI, runtime.AdapterNames())
	mcpIDs := make([]string, 0, len(c.MCPServers))
	for id := range c.MCPServers {
		mcpIDs = append(mcpIDs, id)
	}
	sort.Strings(mcpIDs)
	for _, id := range mcpIDs {
		if err := c.MCPServers[id].Validate(id); err != nil {
			errs = append(errs, err.Error())
		}
	}

	adapterIDs := make([]string, 0, len(c.ExecutorAdapters))
	for id := range c.ExecutorAdapters {
		adapterIDs = append(adapterIDs, id)
	}
	sort.Strings(adapterIDs)
	for _, id := range adapterIDs {
		if err := c.ExecutorAdapters[id].Validate(id); err != nil {
			errs = append(errs, err.Error())
		}
	}

	seenNames := make(map[string]bool, len(c.PipelineAgents))
	for _, a := range c.PipelineAgents {
		validate(a.Name != "", "имя агента обязательно")
		validate(!seenNames[a.Name], "агент %q повторяется в pipeline", a.Name)
		seenNames[a.Name] = true
		if reg != nil && c.Template == "" {
			validate(reg.Exists(a.Name), "агент %q не найден в registry", a.Name)
		}
		validate(isOneOf(a.Effort, "", "low", "medium", "high"),
			"%s: effort %q недопустим (low|medium|high)", a.Name, a.Effort)
		validate(a.CLI == "" || runtime.AdapterExists(a.CLI),
			"%s: cli %q не поддерживается (неизвестный runtime adapter)", a.Name, a.CLI)
		if a.Timeout != "" {
			if duration, err := time.ParseDuration(a.Timeout); err != nil || duration <= 0 {
				errs = append(errs, fmt.Sprintf("%s: timeout %q не парсится (пример: 45m)", a.Name, a.Timeout))
			}
		}
		checkNames := make(map[string]bool, len(a.Checks))
		for _, check := range a.Checks {
			if err := check.Validate(); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", a.Name, err))
			}
			validate(!checkNames[check.Name], "%s: check %q повторяется", a.Name, check.Name)
			checkNames[check.Name] = true
		}
	}
	if validator, ok := reg.(pipelineValidator); ok {
		if err := validator.ValidatePipeline(c.AgentNames()); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.Template != "" {
		if err := c.validateTemplate(reg); err != nil {
			errs = append(errs, strings.TrimPrefix(err.Error(), "невалидный config.yaml:\n  - "))
		}
	} else if c.Workflow != nil {
		for _, edge := range c.Workflow.Edges {
			if edge.Approval == nil {
				continue
			}
			if _, exists := edge.Approval.Actions["approve_spec"]; !exists {
				continue
			}
			if reg == nil {
				continue
			}
			lookup, ok := reg.(productSpecContractLookup)
			if !ok {
				errs = append(errs, fmt.Sprintf("workflow: действие %s.approve_spec требует registry с проверкой outputs proposal; настройте контракт Product Owner approval", edge.From))
				continue
			}
			hasContract, err := lookup.HasProductSpecContract(edge.From)
			if err != nil {
				errs = append(errs, fmt.Sprintf("workflow: проверить contract %s для approve_spec: %v", edge.From, err))
			} else if !hasContract {
				errs = append(errs, fmt.Sprintf("workflow: действие %s.approve_spec требует outputs proposal в .ai-team/agents/%s/def.yaml; используйте обычное действие approve без этого контракта", edge.From, edge.From))
			}
		}
	}
	compiledGraph, graphErr := c.CompiledGraph()
	if graphErr != nil {
		errs = append(errs, graphErr.Error())
	} else if c.Template != "" {
		for _, edge := range compiledGraph.Edges {
			if edge.Approval == nil {
				continue
			}
			for _, role := range edge.Approval.Roles {
				if _, err := cloudidentity.ParseRoles([]string{role}); err != nil {
					errs = append(errs, fmt.Sprintf("workflow: роль approval %q недоступна в cloud", role))
				}
			}
		}
	}
	if c.Tracker != nil {
		stageIDs := make(map[string]bool, len(c.Stages))
		for _, stage := range c.Stages {
			stageIDs[stage.ID] = true
		}
		if err := c.Tracker.Validate(stageIDs); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.CICD != nil {
		if err := c.CICD.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.Notifications != nil {
		stageFunctions := make(map[string]bool, len(c.Stages))
		for _, stage := range c.Stages {
			stageFunctions[stage.Function] = true
		}
		if err := c.Notifications.Validate(stageFunctions); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.Containment != nil {
		if err := c.Containment.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.TreeHash != nil {
		if err := c.TreeHash.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.Budget != nil {
		if err := c.Budget.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := c.AutoPickup.Validate(c.Budget); err != nil {
		errs = append(errs, err.Error())
	}
	if c.Usage != nil {
		if err := c.Usage.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.Redaction != nil {
		if err := c.Redaction.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.Retention != nil {
		if err := c.Retention.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("невалидный config.yaml:\n  - %s", strings.Join(errs, "\n  - "))
	}
	// OPS-2: регистрируем project-specific ignore dirs в каноническом tree
	// hash'е как процесс-глобальный набор. Регистрация происходит только
	// после строгой валидации (TreeHash.Validate выше), поэтому
	// недопустимые имена не могут попасть в глобальный ignore-list.
	if c.TreeHash != nil {
		checks.SetExtraIgnoreDirs(c.TreeHash.IgnoreDirs)
	}
	return nil
}

func isOneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
