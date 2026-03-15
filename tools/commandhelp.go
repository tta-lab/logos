package tools

import _ "embed"

//go:embed commands/read.md
var readHelp string

//go:embed commands/read_md.md
var readMDHelp string

//go:embed commands/read_url.md
var readURLHelp string

//go:embed commands/search.md
var searchHelp string

//go:embed commands/rg.md
var rgHelp string

//go:embed commands/bash.md
var bashHelp string

// CommandHelp holds help text for a command. Single source of truth —
// used by both cobra (--help) and the system prompt builder.
type CommandHelp struct {
	Name    string // display name, e.g. "logos read", "rg"
	Summary string // one-line description (cobra Short)
	Help    string // full help text (cobra Long AND system prompt)
}

var (
	ReadCommand = CommandHelp{
		Name:    "logos read",
		Summary: "Read a file with line numbers",
		Help:    readHelp,
	}
	ReadMDCommand = CommandHelp{
		Name:    "logos read-md",
		Summary: "Read a markdown file with structure awareness",
		Help:    readMDHelp,
	}
	ReadURLCommand = CommandHelp{
		Name:    "logos read-url",
		Summary: "Fetch a URL and return as clean markdown",
		Help:    readURLHelp,
	}
	SearchCommand = CommandHelp{
		Name:    "logos search",
		Summary: "Search the web via DuckDuckGo",
		Help:    searchHelp,
	}
	RGCommand = CommandHelp{
		Name:    "rg",
		Summary: "Search file contents (ripgrep)",
		Help:    rgHelp,
	}
	BashCommand = CommandHelp{
		Name:    "bash",
		Summary: "Standard shell environment",
		Help:    bashHelp,
	}
)

// AllCommands is the full set of available commands.
var AllCommands = []CommandHelp{
	ReadCommand, ReadMDCommand, ReadURLCommand,
	SearchCommand, RGCommand, BashCommand,
}

// SelectCommands returns CommandHelp entries matching the given names.
// Names not found are silently skipped.
func SelectCommands(names ...string) []CommandHelp {
	m := make(map[string]CommandHelp, len(AllCommands))
	for _, c := range AllCommands {
		m[c.Name] = c
	}
	result := make([]CommandHelp, 0, len(names))
	for _, name := range names {
		if c, ok := m[name]; ok {
			result = append(result, c)
		}
	}
	return result
}
