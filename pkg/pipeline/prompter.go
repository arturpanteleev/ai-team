package pipeline

import (
	"bufio"
	"fmt"
	"os"
	"strings"

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
	fmt.Println(question)
	fmt.Print("> ")
	text, err := p.reader.ReadString('\n')
	if err != nil {
		// fail-closed: ошибка чтения трактуется как отказ, а не согласие
		return "n"
	}
	return strings.ToLower(strings.TrimSpace(text))
}
