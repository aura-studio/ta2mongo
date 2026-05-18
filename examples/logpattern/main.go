// Package main demonstrates logPattern glob patterns for both Linux and
// Windows, covering absolute and relative paths.
//
// All patterns use Linux-style forward slashes uniformly. On Windows the
// program auto-converts paths: /c/logs/... → C:\logs\... for directory
// walking, and C:\logs\app.log → /c/logs/app.log for glob matching.
package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// Path conversion helpers (same logic as internal/tailer)
// ---------------------------------------------------------------------------

var driveLetterRe = regexp.MustCompile(`^/([a-zA-Z])/`)

func toWindowsPath(path string) string {
	m := driveLetterRe.FindStringSubmatch(path)
	if m == nil {
		return path
	}
	drive := strings.ToUpper(m[1])
	rest := path[len(m[0]):]
	rest = strings.ReplaceAll(rest, "/", `\`)
	return drive + `:\` + rest
}

func normalizeWindowsPath(path string) string {
	if len(path) >= 2 && path[1] == ':' {
		drive := strings.ToLower(string(path[0]))
		rest := path[2:]
		path = "/" + drive + rest
	}
	return strings.ReplaceAll(path, `\`, "/")
}

// ---------------------------------------------------------------------------
// Glob matching (supports **, *, ?, [...])
// ---------------------------------------------------------------------------

func globMatch(pattern, name string) bool {
	return doGlobMatch(
		strings.Split(pattern, "/"),
		strings.Split(name, "/"),
	)
}

func doGlobMatch(patParts, nameParts []string) bool {
	for len(patParts) > 0 {
		seg := patParts[0]
		if seg == "**" {
			patParts = patParts[1:]
			for len(patParts) > 0 && patParts[0] == "**" {
				patParts = patParts[1:]
			}
			if len(patParts) == 0 {
				return true
			}
			for i := 0; i <= len(nameParts); i++ {
				if doGlobMatch(patParts, nameParts[i:]) {
					return true
				}
			}
			return false
		}
		if len(nameParts) == 0 {
			return false
		}
		matched, _ := filepath.Match(seg, nameParts[0])
		if !matched {
			return false
		}
		patParts = patParts[1:]
		nameParts = nameParts[1:]
	}
	return len(nameParts) == 0
}

// ---------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------

func main() {
	fmt.Println("========================================")
	fmt.Println("  logPattern Glob Matching Examples")
	fmt.Println("========================================")

	// -----------------------------------------------------------------
	// 1. Linux absolute paths
	// -----------------------------------------------------------------
	fmt.Println()
	fmt.Println("--- Linux Absolute Paths ---")
	fmt.Println()

	linuxCases := []struct {
		pattern string
		path    string
	}{
		// * matches any filename
		{"/var/log/app/*.log", "/var/log/app/access.log"},
		{"/var/log/app/*.log", "/var/log/app/error.log"},
		{"/var/log/app/*.log", "/var/log/app/readme.txt"},
		// ** matches any depth
		{"/var/log/**/*.log", "/var/log/app/access.log"},
		{"/var/log/**/*.log", "/var/log/app/sub/deep.log"},
		{"/var/log/**/*.log", "/var/log/sys.log"},
		// ? matches single char
		{"/var/log/app-?.log", "/var/log/app-1.log"},
		{"/var/log/app-?.log", "/var/log/app-12.log"},
		// [...] matches character class
		{"/var/log/app-[0-9].log", "/var/log/app-3.log"},
		{"/var/log/app-[0-9].log", "/var/log/app-x.log"},
	}
	printCases(linuxCases)

	// -----------------------------------------------------------------
	// 2. Linux relative paths
	// -----------------------------------------------------------------
	fmt.Println()
	fmt.Println("--- Linux Relative Paths ---")
	fmt.Println()

	linuxRelCases := []struct {
		pattern string
		path    string
	}{
		{"logs/*.log", "logs/app.log"},
		{"logs/*.log", "logs/sub/app.log"},
		{"logs/**/*.log", "logs/sub/deep/app.log"},
		{"./logs/*.log", "logs/app.log"},
		{"**/*.log", "any/path/deep/app.log"},
		{"*.log", "app.log"},
	}
	printCases(linuxRelCases)

	// -----------------------------------------------------------------
	// 3. Windows absolute paths (written in Linux format, auto-converted)
	// -----------------------------------------------------------------
	fmt.Println()
	fmt.Println("--- Windows Absolute Paths (Linux format → auto conversion) ---")
	fmt.Println()

	winCases := []struct {
		pattern string   // user writes in Linux format
		winPath string   // OS returns Windows path
	}{
		{"/c/logs/*.log", `C:\logs\access.log`},
		{"/c/logs/*.log", `C:\logs\error.log`},
		{"/c/logs/*.log", `C:\logs\readme.txt`},
		{"/d/app/logs/**/*.log", `D:\app\logs\sub\deep.log`},
		{"/c/Program Files/app/*.log", `C:\Program Files\app\error.log`},
	}

	fmt.Printf("  %-40s  %-40s  → converted → %-30s  %s\n",
		"Pattern", "Windows Path", "Normalized", "Match")
	fmt.Println("  " + strings.Repeat("-", 150))
	for _, tc := range winCases {
		normalized := normalizeWindowsPath(tc.winPath)
		// Strip "./" from pattern before matching (same as discoverFiles)
		matchPat := tc.pattern
		if strings.HasPrefix(matchPat, "./") {
			matchPat = matchPat[2:]
		}
		matched := globMatch(matchPat, normalized)
		symbol := "x"
		if matched {
			symbol = "v"
		}
		fmt.Printf("  %-40s  %-40s  → converted → %-30s  [%s]\n",
			tc.pattern, tc.winPath, normalized, symbol)
	}

	// -----------------------------------------------------------------
	// 4. Windows relative paths
	// -----------------------------------------------------------------
	fmt.Println()
	fmt.Println("--- Windows Relative Paths ---")
	fmt.Println()

	winRelCases := []struct {
		pattern string
		winPath string
	}{
		{"logs/*.log", `logs\app.log`},
		{"logs/**/*.log", `logs\sub\deep\app.log`},
		{"**/*.log", `any\path\app.log`},
		{"*.log", `app.log`},
	}

	fmt.Printf("  %-40s  %-40s  → converted → %-30s  %s\n",
		"Pattern", "Windows Path", "Normalized", "Match")
	fmt.Println("  " + strings.Repeat("-", 150))
	for _, tc := range winRelCases {
		normalized := normalizeWindowsPath(tc.winPath)
		matchPat := tc.pattern
		if strings.HasPrefix(matchPat, "./") {
			matchPat = matchPat[2:]
		}
		matched := globMatch(matchPat, normalized)
		symbol := "x"
		if matched {
			symbol = "v"
		}
		fmt.Printf("  %-40s  %-40s  → converted → %-30s  [%s]\n",
			tc.pattern, tc.winPath, normalized, symbol)
	}

	// -----------------------------------------------------------------
	// 5. Path conversion summary
	// -----------------------------------------------------------------
	fmt.Println()
	fmt.Println("--- Path Conversion Summary ---")
	fmt.Println()
	convCases := []struct {
		input string
		desc  string
	}{
		{"/c/logs/app/*.log", "drive C absolute"},
		{"/d/data/**/*.log", "drive D absolute"},
		{"logs/*.log", "relative"},
		{"./logs/**/*.log", "relative with ./"},
		{"**/*.log", "recursive from CWD"},
		{"/var/log/app/*.log", "Linux absolute (no drive)"},
	}
	fmt.Printf("  %-35s  %-15s  → Windows: %s\n", "Linux Pattern", "Type", "Converted")
	fmt.Println("  " + strings.Repeat("-", 80))
	for _, c := range convCases {
		fmt.Printf("  %-35s  %-15s  → Windows: %s\n", c.input, c.desc, toWindowsPath(c.input))
	}
}

func printCases(cases []struct {
	pattern string
	path    string
}) {
	fmt.Printf("  %-40s  %-40s  %s\n", "Pattern", "Path", "Match")
	fmt.Println("  " + strings.Repeat("-", 90))
	for _, tc := range cases {
		// Strip "./" prefix (same as discoverFiles does).
		matchPat := tc.pattern
		if strings.HasPrefix(matchPat, "./") {
			matchPat = matchPat[2:]
		}
		matched := globMatch(matchPat, tc.path)
		symbol := "x"
		if matched {
			symbol = "v"
		}
		fmt.Printf("  %-40s  %-40s  [%s]\n", tc.pattern, tc.path, symbol)
	}
}
