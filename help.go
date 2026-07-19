package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var helpTagRe = regexp.MustCompile(`<[^>]+>`)

// customHelp читает полную инструкцию из HELP_FILE (env) либо help.html рядом с
// бинарём. Пусто означает, что отдельная страница полной инструкции не показывается.
// Файл читается при каждом обращении, поэтому его можно обновлять без рестарта.
func customHelp() string {
	path := os.Getenv("HELP_FILE")
	if path == "" {
		if exe, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(exe), "help.html")
		}
	}
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// helpPlain — версия без HTML-тегов (для MAX, где markdown/HTML не рендерится).
func helpPlain(s string) string {
	return strings.TrimSpace(helpTagRe.ReplaceAllString(s, ""))
}
