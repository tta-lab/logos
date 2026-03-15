Read a file with line numbers.

Flags:
  --offset N   Start from line N (0-based)
  --limit N    Number of lines to read (default 2000)

Examples:
  logos read main.go
  logos read main.go --offset 50 --limit 100

Max file size: 5MB. Lines over 2000 chars are truncated.
Use this instead of cat/head/tail.
