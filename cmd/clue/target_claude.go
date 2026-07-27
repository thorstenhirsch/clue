//go:build !codex

package main

import (
	"github.com/thorstenhirsch/clue/claude"
	"github.com/thorstenhirsch/clue/provider"
)

var activeProvider provider.Provider = claude.NewProvider()

const authCommand = "claude"
