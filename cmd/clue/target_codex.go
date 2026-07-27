//go:build codex

package main

import (
	"github.com/thorstenhirsch/clue/codex"
	"github.com/thorstenhirsch/clue/provider"
)

var activeProvider provider.Provider = codex.NewClient()

const (
	authCommand = "codex login"
	providerID  = "codex"
)
