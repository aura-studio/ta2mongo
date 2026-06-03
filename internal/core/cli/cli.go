// Package cli holds helpers shared by the cmd/* subcommand packages: locating
// the default config file next to the binary.
package cli

import (
	"os"
	"path/filepath"
)

// ResolveConfigPath returns the config file path to use. When flagVal is set
// (the --config flag) it is returned verbatim. Otherwise the first of the
// candidate filenames that exists in the binary's own directory is returned, so
// each subcommand auto-detects its own default (e.g. report.yaml / worker.yaml /
// gateway.yaml / operator.yaml) regardless of the current working directory.
// When none exist it returns "" and the loader falls back to defaults + env + flags.
func ResolveConfigPath(flagVal string, candidates ...string) string {
	if flagVal != "" {
		return flagVal
	}
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}
