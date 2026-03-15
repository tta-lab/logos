package logos

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tta-lab/logos/tools"
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
	assert.Contains(t, result, "# Running Commands")
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
		Date:     "2026-03-12",
		Commands: []tools.CommandHelp{
			{Name: "logos read", Summary: "Read a file", Help: "Read a file with line numbers."},
			{Name: "rg", Summary: "Search files", Help: "Search file contents."},
		},
	}

	result, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, result, "### logos read")
	assert.Contains(t, result, "Read a file with line numbers.")
	assert.Contains(t, result, "### rg")
	assert.Contains(t, result, "Search file contents.")
}

func TestBuildSystemPrompt_NoCommands_EmptySection(t *testing.T) {
	data := PromptData{Platform: "linux", Date: "2026-03-12"}

	result, err := BuildSystemPrompt(data)
	require.NoError(t, err)

	assert.Contains(t, result, "## Available Commands")
	assert.NotContains(t, result, "###") // no command subsections
}
