package logos

import (
	"strings"
	"testing"
)

func TestContainsBlockedCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		// Should block (true)
		{"sed -i basic", "sed -i 's/foo/bar/' file.go", true},
		{"sed -i.bak", "sed -i.bak 's/foo/bar/' file.go", true},
		{"sed -ie", "sed -ie 's/foo/bar/' file.go", true},
		{"sed --in-place", "sed --in-place 's/foo/bar/' file.go", true},
		{"sed -ni combined flags", "sed -ni '/pattern/p' file.go", true},
		{"perl -i -pe", "perl -i -pe 's/foo/bar/' file.go", true},
		{"perl -i.bak", "perl -i.bak -pe 's/foo/bar/' file.go", true},
		{"perl -pie", "perl -pie 's/foo/bar/' file.go", true},
		{"perl -pi -e", "perl -pi -e 's/foo/bar/' file.go", true},
		{"sed -i after &&", "ls && sed -i 's/x/y/' f", true},
		{"perl -i after pipe", "grep foo | perl -pi -e 's/x/y/'", true},
		{"sed -i after semicolon", "echo hi; sed -i 's/x/y/' f", true},
		// Should NOT block (false)
		{"sed without -i", "sed 's/foo/bar/' file.go", false},
		{"sed -n no i", "sed -n '/pattern/p' file.go", false},
		{"perl without -i", `perl -e 'print "hello"'`, false},
		{"perl -n no i", "perl -ne 'print if /foo/' file", false},
		{"awk print", "awk '{print $1}' file", false},
		{"sed -i in quoted string", `echo "use sed -i to edit"`, false},
		{"python3", "python3 script.py", false},
		{"cat", "cat file.go", false},
		{"grep -i pattern", "grep -i pattern file", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainsBlockedCommand(tt.cmd); got != tt.want {
				t.Errorf("ContainsBlockedCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestBlockedCommandDirective(t *testing.T) {
	directive := blockedCommandDirective("sed -i 's/foo/bar/'")
	if directive == "" {
		t.Fatal("blockedCommandDirective returned empty string")
	}
	if !strings.Contains(directive, "src edit") {
		t.Errorf("directive should contain 'src edit', got: %s", directive)
	}
}
