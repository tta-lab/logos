package logos

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSystemPrompt_AllFields(t *testing.T) {
	data := PromptData{
		WorkingDir: "/home/user/project",
		Platform:   "linux",
		Date:       "2026-03-12",
	}

	result, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, result, "/home/user/project")
	assert.Contains(t, result, "linux")
	assert.Contains(t, result, "2026-03-12")
	assert.Contains(t, result, "# Command Mode")
}

func TestBuildSystemPrompt_EmptyWorkingDir_OmitsIt(t *testing.T) {
	data := PromptData{
		Platform: "darwin",
		Date:     "2026-03-12",
	}

	result, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	assert.NotContains(t, result, "Working directory")
}

func TestBuildSystemPrompt_ContainsEnvironmentSection(t *testing.T) {
	data := PromptData{
		WorkingDir: "/project",
		Platform:   "linux",
		Date:       "2026-03-12",
	}

	result, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, result, "# Environment")
	assert.Contains(t, result, "/project")
}

func TestBuildSystemPrompt_ReturnsNonEmptyString(t *testing.T) {
	result, err := BuildSystemPrompt(PromptData{})
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(result))
}

func TestSystemPromptComposition_AppendsConsumerInstructions(t *testing.T) {
	data := PromptData{WorkingDir: "/project", Platform: "linux", Date: "2026-03-12"}

	base, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	consumer := "You are a code reviewer."
	combined := base + "\n\n" + consumer

	assert.Contains(t, combined, "# Environment")
	assert.Contains(t, combined, consumer)
	assert.Greater(t, strings.Index(combined, consumer),
		strings.Index(combined, "# Environment"))
}

func TestBuildSystemPrompt_WithCommands(t *testing.T) {
	data := PromptData{
		Platform: "linux",
		Date:     "2026-03-23",
		Commands: []CommandDoc{
			{
				Name:    "url",
				Summary: "Fetch a web page as markdown",
				Help:    "Flags:\n  --tree   Show heading tree\n  -s ID    Read section by ID",
			},
			{Name: "web", Summary: "Search the web", Help: "Flags:\n  -n N   Max results (default 10)"},
		},
	}
	result, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, result, "# Available Commands")
	assert.Contains(t, result, "## url")
	assert.Contains(t, result, "Fetch a web page as markdown")
	assert.Contains(t, result, "--tree   Show heading tree")
	assert.Contains(t, result, "## web")
	assert.Contains(t, result, "Search the web")
	assert.NotContains(t, result, "temenos")
}

func TestBuildSystemPrompt_NoCommands_NoAvailableSection(t *testing.T) {
	data := PromptData{
		Platform: "linux",
		Date:     "2026-03-23",
	}
	result, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	assert.NotContains(t, result, "# Available Commands")
	assert.Contains(t, result, "# Command Mode")
}

func TestBuildSystemPrompt_CommandOrder_Preserved(t *testing.T) {
	data := PromptData{
		Platform: "linux",
		Date:     "2026-03-23",
		Commands: []CommandDoc{
			{Name: "rg", Summary: "Search file contents", Help: "ripgrep"},
			{Name: "url", Summary: "Fetch web page", Help: "fetch"},
		},
	}
	result, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	rgIdx := strings.Index(result, "## rg")
	urlIdx := strings.Index(result, "## url")
	assert.Greater(t, urlIdx, rgIdx, "commands should render in provided order")
}
