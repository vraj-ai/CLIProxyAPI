package main

import (
	_ "embed"
	"strings"
)

//go:embed theme.css
var themeCSS string

//go:embed hub.html
var hubPageHTML string

//go:embed savings.html
var savingsPageHTML string

//go:embed router.html
var routerPageHTML string

//go:embed keys.html
var keysPageHTML string

func withTheme(raw string) string {
	return strings.Replace(raw, "/*THEME*/", themeCSS, 1)
}
