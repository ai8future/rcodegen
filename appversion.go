package rcodegen

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var rawAppVersion string

var AppVersion = strings.TrimSpace(rawAppVersion)
