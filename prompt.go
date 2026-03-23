package logos

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed system.md.tpl
var systemPromptTemplate string

// systemPromptTmpl is parsed once at init — surfaces syntax errors at startup.
var systemPromptTmpl = template.Must(template.New("system").Parse(systemPromptTemplate))

// CommandDoc describes a command available to the agent.
// Callers provide these to control which commands appear in the system prompt.
type CommandDoc struct {
	Name    string // command name, e.g. "url", "web", "rg"
	Summary string // one-line description shown under the heading
	Help    string // full help text (flags, examples, caveats)
}

// PromptData holds the runtime context used to render the default system prompt.
type PromptData struct {
	WorkingDir string
	Platform   string
	Date       string
	Commands   []CommandDoc // caller-provided command documentation
}

// BuildSystemPrompt renders the default system prompt with runtime context.
// The result is the base prompt — consumers append their own instructions after this.
func BuildSystemPrompt(data PromptData) (string, error) {
	var buf strings.Builder
	if err := systemPromptTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute system prompt template: %w", err)
	}
	return buf.String(), nil
}
