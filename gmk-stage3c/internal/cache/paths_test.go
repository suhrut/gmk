package cache

import (
	"path/filepath"
	"testing"
)

func TestCacheDir(t *testing.T) {
	got := CacheDir("/home/user/proj")
	want := filepath.Join("/home/user/proj", ".gmk-cache")
	if got != want {
		t.Errorf("CacheDir = %q, want %q", got, want)
	}
}

func TestCodeDir(t *testing.T) {
	got := CodeDir("/home/user/proj")
	want := filepath.Join("/home/user/proj", ".gmk-cache", "code")
	if got != want {
		t.Errorf("CodeDir = %q, want %q", got, want)
	}
}

func TestScriptPath(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		source string
		target string
		lang   string
		want   string
	}{
		{
			name:   "simple bash, gmk.yml at root",
			root:   "/p",
			source: "/p/gmk.yml",
			target: "hello",
			lang:   "bash",
			want:   filepath.Join("/p", ".gmk-cache", "code", "local", "gmk.yml", "hello.sh"),
		},
		{
			name:   "python, nested source",
			root:   "/p",
			source: "/p/scripts/gmk.yml",
			target: "compile",
			lang:   "python",
			want:   filepath.Join("/p", ".gmk-cache", "code", "local", "scripts", "gmk.yml", "compile.py"),
		},
		{
			name:   "empty lang defaults to bash",
			root:   "/p",
			source: "/p/gmk.yml",
			target: "t",
			lang:   "",
			want:   filepath.Join("/p", ".gmk-cache", "code", "local", "gmk.yml", "t.sh"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ScriptPath(tc.root, tc.source, tc.target, tc.lang)
			if got != tc.want {
				t.Errorf("ScriptPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtensionForLang(t *testing.T) {
	tests := []struct {
		lang, want string
	}{
		{"bash", ".sh"},
		{"sh", ".sh"},
		{"", ".sh"},
		{"python", ".py"},
		{"python3", ".py"},
		{"perl", ".pl"},
		{"ruby", ".rb"},
		{"node", ".mjs"},
		{"nodejs", ".mjs"},
		{"pwsh", ".ps1"},
		{"powershell", ".ps1"},
		{"cmd", ".cmd"},
		{"unknown_lang", ".sh"},
	}
	for _, tc := range tests {
		t.Run(tc.lang, func(t *testing.T) {
			if got := ExtensionForLang(tc.lang); got != tc.want {
				t.Errorf("ExtensionForLang(%q) = %q, want %q", tc.lang, got, tc.want)
			}
		})
	}
}

func TestInterpreterForLang(t *testing.T) {
	tests := []struct {
		lang, want string
	}{
		{"bash", "bash"},
		{"", "bash"},
		{"sh", "sh"},
		{"python", "python3"},
		{"python3", "python3"},
		{"perl", "perl"},
		{"ruby", "ruby"},
		{"node", "node"},
		{"nodejs", "node"},
		{"pwsh", "pwsh"},
		{"powershell", "pwsh"},
		{"cmd", "cmd"},
		{"unknown", "bash"},
	}
	for _, tc := range tests {
		t.Run(tc.lang, func(t *testing.T) {
			if got := InterpreterForLang(tc.lang); got != tc.want {
				t.Errorf("InterpreterForLang(%q) = %q, want %q", tc.lang, got, tc.want)
			}
		})
	}
}
