package main

import (
	_ "embed"
	"strings"
)

//go:embed theme.css
var themeCSS string

//go:embed shell.html
var hubPageHTML string

func withTheme(raw string) string {
	raw = strings.Replace(raw, "/*THEME*/", themeCSS, 1)
	return strings.Replace(raw, "/*KEYRING*/", consoleKeyJS(), 1)
}
