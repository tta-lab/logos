package logos

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatOneResult(t *testing.T) {
	t.Run("exit -1 suppresses exit code line", func(t *testing.T) {
		out := formatOneResult(Result{Command: "sleep 5", ExitCode: -1, Stdout: "timed out"})
		if strings.Contains(out, "exit code") {
			t.Errorf("exit code line should be suppressed for -1, got: %q", out)
		}
	})
}

func TestFormatResults(t *testing.T) {
	tests := []struct {
		name              string
		results           []Result
		wantContains      []string
		expectNotContains []string
		wantEmpty         bool
	}{
		{
			name:      "empty results",
			results:   nil,
			wantEmpty: true,
		},
		{
			name: "single result exit 0 stdout only",
			results: []Result{{
				Command:  "ls",
				Stdout:   "file1\nfile2",
				ExitCode: 0,
			}},
			wantContains:      []string{"<result>", "ls", "file1\nfile2", "</result>"},
			expectNotContains: []string{"exit code"},
		},
		{
			name: "single result non-zero exit",
			results: []Result{{
				Command:  "false",
				Stdout:   "",
				ExitCode: 1,
			}},
			wantContains:      []string{"(exit code: 1)"},
			expectNotContains: []string{"(exit code: -1)"},
		},
		{
			name: "single result exit -1",
			results: []Result{{
				Command:  "sleep 5",
				ExitCode: -1,
				Stdout:   "timed out",
			}},
			expectNotContains: []string{"(exit code: -1)"},
		},
		{
			name: "single result empty stdout stderr",
			results: []Result{{
				Command:  "true",
				Stdout:   "",
				Stderr:   "",
				ExitCode: 0,
			}},
			wantContains: []string{"(no output)"},
		},
		{
			name: "single result stdout stderr",
			results: []Result{{
				Command:  "cmd",
				Stdout:   "stdout",
				Stderr:   "stderr",
				ExitCode: 0,
			}},
			wantContains: []string{"stdout", "STDERR:\nstderr"},
		},
		{
			name: "single result empty stdout non-empty stderr",
			results: []Result{{
				Command:  "cmd",
				Stdout:   "",
				Stderr:   "err",
				ExitCode: 0,
			}},
			wantContains: []string{"STDERR:\nerr"},
		},
		{
			name: "single result err set",
			results: []Result{{
				Command: "bad",
				Stdout:  "ignored",
				Err:     assert.AnError,
			}},
			wantContains:      []string{"execution error: "},
			expectNotContains: []string{"ignored"},
		},
		{
			name: "multiple results joined",
			results: []Result{
				{Command: "a", Stdout: "out1", ExitCode: 0},
				{Command: "b", Stdout: "out2", ExitCode: 0},
			},
			wantContains: []string{"a", "out1", "b", "out2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := FormatResults(tt.results)
			if tt.wantEmpty {
				assert.Equal(t, "", out)
				return
			}
			assert.NotEmpty(t, out)
			for _, sub := range tt.wantContains {
				assert.Contains(t, out, sub)
			}
			for _, sub := range tt.expectNotContains {
				assert.NotContains(t, out, sub)
			}
		})
	}
}
