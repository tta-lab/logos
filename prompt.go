package logos

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/tta-lab/temenos/tools"
)

//go:embed system.md.tpl
var systemPromptTemplate string

// systemPromptTmpl is parsed once at init — surfaces syntax errors at startup.
var systemPromptTmpl = template.Must(template.New("system").Parse(systemPromptTemplate))

// CommandHelp re-exports tools.CommandHelp so consumers don't import temenos/tools directly.
type CommandHelp = tools.CommandHelp

// AllCommands returns the full set of available commands.
// Re-exported from temenos/tools.
func AllCommands() []CommandHelp {
	return tools.AllCommands
}

// PromptData holds the runtime context used to render the default system prompt.
type PromptData struct {
	WorkingDir string
	Platform   string
	Date       string
	Commands   []CommandHelp // nil = all commands; use AllCommands() or tools.SelectCommands() to customize
}

// BuildSystemPrompt renders the default system prompt with runtime context.
// The result is the base prompt — consumers append their own instructions after this.
func BuildSystemPrompt(data PromptData) (string, error) {
	if data.Commands == nil {
		data.Commands = tools.AllCommands
	}
	var buf strings.Builder
	if err := systemPromptTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute system prompt template: %w", err)
	}
	return buf.String(), nil
}
