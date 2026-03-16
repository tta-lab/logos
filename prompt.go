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

// PromptData holds the runtime context used to render the default system prompt.
type PromptData struct {
	WorkingDir string
	Platform   string
	Date       string
	Network    bool // include read-url + search docs
	ReadFS     bool // include rg + read-only filesystem docs
}

// BuildSystemPrompt renders the default system prompt with runtime context.
// The result is the base prompt — consumers append their own instructions after this.
func BuildSystemPrompt(data PromptData) (string, error) {
	// Build command list from capability switches.
	var commands []tools.CommandHelp
	if data.Network {
		commands = append(commands, tools.ReadURLCommand, tools.SearchCommand)
	}
	if data.ReadFS {
		commands = append(commands, tools.RGCommand)
	}

	tplData := promptTplData{
		WorkingDir: data.WorkingDir,
		Platform:   data.Platform,
		Date:       data.Date,
		Commands:   commands,
		Network:    data.Network,
		ReadFS:     data.ReadFS,
	}

	var buf strings.Builder
	if err := systemPromptTmpl.Execute(&buf, tplData); err != nil {
		return "", fmt.Errorf("execute system prompt template: %w", err)
	}
	return buf.String(), nil
}

// promptTplData is the internal template data — keeps PromptData public API clean.
type promptTplData struct {
	WorkingDir string
	Platform   string
	Date       string
	Commands   []tools.CommandHelp
	Network    bool
	ReadFS     bool
}
