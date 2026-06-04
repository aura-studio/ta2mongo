package tailer

import (
	"runtime"
	"strings"
	"testing"
)

func TestLogPatternExamples_GlobMatching(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"linux absolute exact", "/var/log/app/*.log", "/var/log/app/access.log", true},
		{"linux absolute extension mismatch", "/var/log/app/*.log", "/var/log/app/readme.txt", false},
		{"linux recursive nested", "/var/log/**/*.log", "/var/log/app/sub/deep.log", true},
		{"linux recursive same dir", "/var/log/**/*.log", "/var/log/sys.log", true},
		{"single char wildcard", "/var/log/app-?.log", "/var/log/app-1.log", true},
		{"single char wildcard too long", "/var/log/app-?.log", "/var/log/app-12.log", false},
		{"char class", "/var/log/app-[0-9].log", "/var/log/app-3.log", true},
		{"char class mismatch", "/var/log/app-[0-9].log", "/var/log/app-x.log", false},
		{"relative one segment", "logs/*.log", "logs/app.log", true},
		{"relative one segment nested", "logs/*.log", "logs/sub/app.log", false},
		{"relative recursive", "logs/**/*.log", "logs/sub/deep/app.log", true},
		{"recursive anywhere", "**/*.log", "any/path/deep/app.log", true},
		{"root relative file", "*.log", "app.log", true},
		{"dot slash stripped", strings.TrimPrefix("./logs/*.log", "./"), "logs/app.log", true},
		{"parent prefix preserved", "../logs/*.log", "../logs/app.log", true},
		{"parent recursive", "../logs/**/*.log", "../logs/sub/deep/app.log", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := globMatch(tt.pattern, tt.path); got != tt.want {
				t.Fatalf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestLogPatternExamples_WindowsPathMatching(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		osPath  string
		want    bool
	}{
		{"drive pattern matches access log", "/c/logs/*.log", `C:\logs\access.log`, true},
		{"drive pattern rejects txt", "/c/logs/*.log", `C:\logs\readme.txt`, false},
		{"recursive drive pattern", "/d/app/logs/**/*.log", `D:\app\logs\sub\deep.log`, true},
		{"space in path", "/c/Program Files/app/*.log", `C:\Program Files\app\error.log`, true},
		{"native backslash pattern", `C:\logs\*.log`, `C:\logs\access.log`, true},
		{"native backslash pattern rejects txt", `C:\logs\*.log`, `C:\logs\readme.txt`, false},
		{"relative backslash path", "logs/*.log", `logs\app.log`, true},
		{"relative recursive backslash path", "logs/**/*.log", `logs\sub\deep\app.log`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := normalizeWindowsPath(tt.pattern)
			path := normalizeWindowsPath(tt.osPath)
			if got := globMatch(pattern, path); got != tt.want {
				t.Fatalf("globMatch(%q, %q) = %v, want %v", pattern, path, got, tt.want)
			}
		})
	}
}

func TestLogPatternExamples_PathConversion(t *testing.T) {
	if got := toWindowsPath("/c/logs/app/*.log"); got != `C:\logs\app\*.log` {
		t.Fatalf("toWindowsPath = %q", got)
	}
	if got := toWindowsPath("/var/log/app/*.log"); got != "/var/log/app/*.log" {
		t.Fatalf("toWindowsPath linux absolute = %q", got)
	}
	if got := normalizeWindowsPath(`C:\logs\access.log`); got != "/c/logs/access.log" {
		t.Fatalf("normalizeWindowsPath = %q", got)
	}
	if runtime.GOOS != "windows" {
		if got := normalizePath("logs/app.log"); got != "logs/app.log" {
			t.Fatalf("normalizePath on non-windows = %q", got)
		}
	}
}
