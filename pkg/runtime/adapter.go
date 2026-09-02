package runtime

import (
	"fmt"
	"io"
	"sort"
	"sync"
)

// RuntimeAdapter — тонкий контракт между ai-team контроллером и внешним
// CLI-харнессом. Контракт каноничен и НЕ содержит vendor-specific полей:
// конкретика argv, env-изоляции, permission-формата и результата живёт
// внутри реализации адаптера (например pkg/runtime/opencode.go).
//
// Контракт отделяет пять аспектов запуска:
//   - identity:       Descriptor (name + binary);
//   - capabilities:   Capability-декларация (Describe);
//   - launch:         Command (argv конкретной модели/файла промпта);
//   - policy mapping: Validate + Environment (маппинг политики этапа в
//     изоляцию сессии харнесса);
//   - usage:          опциональный UsageSource с attested-данными расхода.
//
// Неизвестная capability при fail-closed (strict) раскрытии блокирует запуск,
// а не молча деградирует: см. ValidateLaunch.
type RuntimeAdapter interface {
	// Name — registry-ключ адаптера: бинарник харнесса (например "opencode").
	Name() string

	// Describe — статическая декларация возможностей адаптера.
	Describe() Descriptor

	// Validate — fail-closed проверка запрошенной политики этапа (Launch).
	// Реализации возвращают ошибку от ValidateLaunch, если харнесс не
	// декларирует capability, обязательную для запроса.
	Validate(launch Launch) error

	// Command — argv запуска харнесса. Launch несёт model/effort политику этапа
	// (adapter маппит её в харнесс-специфичные флаги); cli — бинарник;
	// promptFile — временный файл 0600 с промптом. Адаптер, принимающий промпт
	// через stdin (codex exec -), ожидает, что оркестратор направляет
	// promptFile в cmd.Stdin.
	Command(cli string, launch Launch, promptFile string) ([]string, error)

	// Environment — окружение субпроцесса с изоляцией сессии (allow-list env,
	// запрет эффектных инструментов, суженные edit-scope). Возвращает cleanup,
	// удаляющий временные ресурсы после запуска.
	Environment(agent *Agent, task *Task, inputs ...Artifact) ([]string, func(), error)
}

// Capability — свойство внешнего харнесса, объявленное адаптером. Кандидаты
// на неизвестную capability при strict-раскрытии блокируют запуск.
type Capability string

const (
	// CapModelSelection — харнесс умеет выбирать модель (--model / -m).
	CapModelSelection Capability = "model"
	// CapEffortMapping — харнесс принимает уровень усилий low/medium/high.
	CapEffortMapping Capability = "effort"
	// CapPromptFile — харнесс принимает большой промпт отдельным файлом
	// (не через argv), избегая ARG_MAX и случайного продолжения сессии.
	CapPromptFile Capability = "prompt-file"
	// CapSessionIsolation — харнесс кооперируется с изоляцией агентной
	// сессии (allow-list env, запрет эффектных инструментов, narrow edit
	// scope). Требуется для любых agent-стадий и eval-судьи.
	CapSessionIsolation Capability = "session-isolation"
	// CapUsageReported — харнесс отдаёт фактический расход (tokens/cost) в
	// структурированном результате; адаптер реализует UsageSource.
	CapUsageReported Capability = "usage-reported"
)

// Descriptor — статическая декларация адаптера.
type Descriptor struct {
	Name         string       `json:"name"`
	Binary       string       `json:"binary"`
	Capabilities []Capability `json:"capabilities"`
	// PromptViaStdin — харнесс читает промпт из stdin (codex exec -).
	// True: оркестратор направляет promptFile в cmd.Stdin. False (opencode):
	// промпт передаётся только через argv --file, stdin остаётся не тронутым
	// (иначе opencode получил бы промпт дважды).
	PromptViaStdin bool `json:"prompt_via_stdin,omitempty"`
}

// Launch — harness-neutral запрос на запуск этапа. Политика этапа маппится
// адаптером; capability, которой у харнесса нет, при strict-раскрытии
// блокирует запуск (fail-closed).
type Launch struct {
	Model        string
	Effort       string
	Interactive  bool
	AskQuestions bool
	// RequireIsolation — этап обязан выполняться под изоляцией сессии
	// (agent-стадии и eval-судья всегда требуют её).
	RequireIsolation bool
}

// Usage — фактический расход одного запуска харнесса. Поля принимаются ТОЛЬКО
// от адаптера, аттестующего источник данных (see CapUsageReported / P1-7);
// контроллер никогда не угадывает usage сам.
type Usage struct {
	TokensInput int64 `json:"tokens_input,omitempty"`
	// CachedInputTokens — доля входных токенов, прочитанных из кэша харнесса.
	// По конвенции OpenAI кэшированные входные токены уже входят в
	// TokensInput; это поле ведётся отдельно для учёта скидки cache-read
	// при расчёте cost, но НЕ суммируется с TokensInput.
	CachedInputTokens int64   `json:"cached_input_tokens,omitempty"`
	TokensOutput      int64   `json:"tokens_output,omitempty"`
	CostUSD           float64 `json:"cost_usd,omitempty"`
	Currency          string  `json:"currency,omitempty"`
	Attested          bool    `json:"attested"`
}

// UsageSource — опциональный интерфейс адаптера: разбирает usage из
// структурированного результата запуска. Wiring в evidence — P1-7.
type UsageSource interface {
	ParseUsage(reader io.Reader) (*Usage, error)
}

// Registry — реестр адаптеров по имени бинарника харнесса.
type Registry struct {
	adapters map[string]RuntimeAdapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]RuntimeAdapter)}
}

// Register регистрирует адаптер. Дубликат имени — паника: это программная
// ошибка конфигурации билда, а не пользовательский ввод.
func (r *Registry) Register(a RuntimeAdapter) {
	if a == nil || a.Name() == "" {
		panic("runtime: adapter без имени")
	}
	if _, exists := r.adapters[a.Name()]; exists {
		panic(fmt.Sprintf("runtime: адаптер %q уже зарегистрирован", a.Name()))
	}
	r.adapters[a.Name()] = a
}

// Lookup достаёт адаптер по имени. Неизвестное имя возвращает ошибку:
// запуск харнесса через guessed arguments запрещён (fail-closed).
func (r *Registry) Lookup(name string) (RuntimeAdapter, error) {
	a, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("runtime: неизвестный adapter %q (зарегистрированы: %s)",
			name, joinNames(r.adapterNames()))
	}
	return a, nil
}

// Exists сообщает, зарегистрирован ли адаптер по имени.
func (r *Registry) Exists(name string) bool {
	_, ok := r.adapters[name]
	return ok
}

func (r *Registry) adapterNames() []string {
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func joinNames(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += "\"" + name + "\""
	}
	if out == "" {
		return "—"
	}
	return out
}

var (
	defaultRegistry   = NewRegistry()
	adapterRegistryMu sync.Mutex
)

// RegisterAdapter регистрирует адаптер в default-реестре (см. init() адаптеров).
func RegisterAdapter(a RuntimeAdapter) {
	adapterRegistryMu.Lock()
	defer adapterRegistryMu.Unlock()
	defaultRegistry.Register(a)
}

// Adapter — lookup в default-реестре.
func Adapter(name string) (RuntimeAdapter, error) {
	adapterRegistryMu.Lock()
	defer adapterRegistryMu.Unlock()
	return defaultRegistry.Lookup(name)
}

// AdapterExists — зарегистрирован ли адаптер с данным именем.
func AdapterExists(name string) bool {
	adapterRegistryMu.Lock()
	defer adapterRegistryMu.Unlock()
	return defaultRegistry.Exists(name)
}

// AdapterNames — отсортированный список зарегистрированных адаптеров.
func AdapterNames() []string {
	adapterRegistryMu.Lock()
	defer adapterRegistryMu.Unlock()
	return defaultRegistry.adapterNames()
}

// RequiredCapabilities выводит набор capability, контрактно обязательных для
// запрошенного Launch. Это единственное место, где политика этапа маппится в
// capability-пространство: адаптеры не дублируют эту логику.
func RequiredCapabilities(launch Launch) map[Capability]bool {
	required := map[Capability]bool{CapPromptFile: true}
	if launch.Model != "" {
		required[CapModelSelection] = true
	}
	if launch.Effort != "" {
		required[CapEffortMapping] = true
	}
	if launch.Interactive || launch.AskQuestions || launch.RequireIsolation {
		required[CapSessionIsolation] = true
	}
	return required
}

// ValidateLaunch — общая fail-closed проверка для любого адаптера: capability,
// обязательные для Launch, но не декларированные адаптером, блокируют запуск.
// Ошибка возвращается как *MissingCapabilitiesError.
func ValidateLaunch(a RuntimeAdapter, launch Launch) error {
	return ValidateLaunchWith(a, launch, nil)
}

// ValidateLaunchWith — как ValidateLaunch, но с дополнительным явным набором
// capability поверх маппинга Launch (для запросов, не выразимых полем Launch).
func ValidateLaunchWith(a RuntimeAdapter, launch Launch, extra []Capability) error {
	declared := make(map[Capability]bool)
	for _, capability := range a.Describe().Capabilities {
		declared[capability] = true
	}
	required := RequiredCapabilities(launch)
	for _, capability := range extra {
		required[capability] = true
	}
	var missing []Capability
	for capability := range required {
		if !declared[capability] {
			missing = append(missing, capability)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return &MissingCapabilitiesError{AdapterName: a.Name(), Missing: missing}
}

// MissingCapabilitiesError — систематизированная fail-closed ошибка: у харнесса
// нет capability, обязательных для запроса. Позволяет контроллеру отличать
// отказ по capability от прочих ошибок запуска.
type MissingCapabilitiesError struct {
	AdapterName string
	Missing     []Capability
}

func (e *MissingCapabilitiesError) Error() string {
	names := make([]string, 0, len(e.Missing))
	for _, capability := range e.Missing {
		names = append(names, string(capability))
	}
	return fmt.Sprintf("runtime: харнесс %q не декларирует capability %s, обязательные для запроса (fail-closed)",
		e.AdapterName, joinNames(names))
}
