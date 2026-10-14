package pipeline

import (
	"bufio"
	"os"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/mattn/go-isatty"
)

// Prompter абстрагирует интерактивные вопросы пайплайна: в проде — консоль,
// в тестах — скриптованные ответы, в CI — неинтерактивная заглушка.
type Prompter interface {
	// Interactive сообщает, можно ли вообще задавать вопросы.
	Interactive() bool
	// Ask печатает вопрос и возвращает ответ (trimmed, lowercase).
	Ask(question string) string
}

// ConsolePrompter — stdin/stdout реализация.
type ConsolePrompter struct {
	reader *bufio.Reader
}

func NewConsolePrompter() *ConsolePrompter {
	return &ConsolePrompter{reader: bufio.NewReader(os.Stdin)}
}

// Interactive: настоящий TTY, а не просто char device — /dev/null тоже
// символьное устройство, но вопросы задавать некому.
func (p *ConsolePrompter) Interactive() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func (p *ConsolePrompter) Ask(question string) string {
	logging.Printf("%s\n", question)
	logging.Printf("> ")
	text, err := p.reader.ReadString('\n')
	if err != nil {
		// fail-closed: ошибка чтения трактуется как отказ, а не согласие
		return "n"
	}
	return strings.ToLower(strings.TrimSpace(text))
}

// NonInteractivePrompter is used by the web server and its controller. Human
// decisions there arrive through the web API, so the pipeline must never block
// on the process stdin: when a terminal is attached, ConsolePrompter would wait
// for a console answer and the run would never leave the approval checkpoint.
type NonInteractivePrompter struct{}

func (NonInteractivePrompter) Interactive() bool { return false }

// Ask is never reached on the non-interactive path; it refuses by default.
func (NonInteractivePrompter) Ask(string) string { return "n" }
