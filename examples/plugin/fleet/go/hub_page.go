package main

import (
	_ "embed"
)

//go:embed hub.html
var hubPageHTML string

// keep the embed close to render so the asset lives in the binary but edits
// stay in a real file.
