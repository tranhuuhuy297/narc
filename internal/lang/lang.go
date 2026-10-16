// Package lang detects languages and splits source lines into code,
// comments and string literals so rules can match code without being fooled
// by text inside strings or comments.
package lang

import (
	"path"
	"strings"
)

// Lang identifies a language (or file format).
type Lang int

const (
	Unknown Lang = iota
	JavaScript
	TypeScript
	Python
	Go
	Rust
	Java
	Kotlin
	CSharp
	Swift
	Scala
	Groovy
	Dart
	C
	Cpp
	PHP
	Ruby
	Elixir
	Shell
	YAML
	JSON // JSON and JSONC (comments allowed)
	TOML
	INI
	Makefile
	Dockerfile
	Markdown
)

var names = map[Lang]string{
	Unknown: "unknown", JavaScript: "javascript", TypeScript: "typescript", Python: "python",
	Go: "go", Rust: "rust", Java: "java", Kotlin: "kotlin", CSharp: "csharp", Swift: "swift",
	Scala: "scala", Groovy: "groovy", Dart: "dart", C: "c", Cpp: "cpp", PHP: "php", Ruby: "ruby",
	Elixir: "elixir", Shell: "shell", YAML: "yaml", JSON: "json", TOML: "toml", INI: "ini",
	Makefile: "makefile", Dockerfile: "dockerfile", Markdown: "markdown",
}

func (l Lang) String() string { return names[l] }

// IsJSLike reports whether l is JavaScript or TypeScript.
func (l Lang) IsJSLike() bool { return l == JavaScript || l == TypeScript }

// IsProgramming reports whether l is a general-purpose programming language
// (as opposed to a config/data format).
func (l Lang) IsProgramming() bool {
	switch l {
	case Unknown, Shell, YAML, JSON, TOML, INI, Makefile, Dockerfile, Markdown:
		return false
	}
	return true
}

type family int

const (
	famNone family = iota // no lexing; everything is code
	famC                  // // and /* */ comments
	famHash               // # comments
)

func (l Lang) family() family {
	switch l {
	case JavaScript, TypeScript, Go, Rust, Java, Kotlin, CSharp, Swift, Scala, Groovy, Dart, C, Cpp, PHP, JSON:
		return famC
	case Python, Ruby, Elixir, Shell, YAML, TOML, INI, Makefile, Dockerfile:
		return famHash
	}
	return famNone
}

var extLang = map[string]Lang{
	".js": JavaScript, ".jsx": JavaScript, ".mjs": JavaScript, ".cjs": JavaScript,
	".ts": TypeScript, ".tsx": TypeScript, ".mts": TypeScript, ".cts": TypeScript,
	".py": Python, ".pyi": Python,
	".go":   Go,
	".rs":   Rust,
	".java": Java,
	".kt":   Kotlin, ".kts": Kotlin,
	".cs":    CSharp,
	".swift": Swift,
	".scala": Scala, ".sc": Scala,
	".groovy": Groovy, ".gradle": Groovy,
	".dart": Dart,
	".c":    C, ".h": C,
	".cc": Cpp, ".cpp": Cpp, ".cxx": Cpp, ".hpp": Cpp, ".hh": Cpp, ".hxx": Cpp,
	".php": PHP,
	".rb":  Ruby, ".rake": Ruby,
	".ex": Elixir, ".exs": Elixir,
	".sh": Shell, ".bash": Shell, ".zsh": Shell,
	".yml": YAML, ".yaml": YAML,
	".json": JSON, ".jsonc": JSON, ".json5": JSON,
	".toml": TOML,
	".ini":  INI, ".cfg": INI, ".coveragerc": INI,
	".mk": Makefile,
	".md": Markdown, ".markdown": Markdown, ".mdx": Markdown,
}

var baseLang = map[string]Lang{
	"makefile": Makefile, "gnumakefile": Makefile,
	"dockerfile": Dockerfile, "containerfile": Dockerfile,
	"jenkinsfile": Groovy,
	".coveragerc": INI, "setup.cfg": INI, "tox.ini": INI, "pytest.ini": INI, ".flake8": INI, ".pylintrc": INI, "pylintrc": INI,
	".nycrc": JSON, ".babelrc": JSON, ".eslintrc": JSON, ".swcrc": JSON, ".prettierrc": JSON,
	"gemfile": Ruby, "rakefile": Ruby, "podfile": Ruby,
	"justfile": Makefile,
}

// Detect returns the language for a file path.
func Detect(p string) Lang {
	base := strings.ToLower(path.Base(p))
	if l, ok := baseLang[base]; ok {
		return l
	}
	if strings.HasPrefix(base, "dockerfile") {
		return Dockerfile
	}
	if l, ok := extLang[strings.ToLower(path.Ext(base))]; ok {
		return l
	}
	return Unknown
}
