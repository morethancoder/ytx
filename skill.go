// Package ytx embeds the files the binary carries (SKILL.md for `ytx schema`).
package ytx

import _ "embed"

// Skill is SKILL.md, printed by `ytx schema` so an agent learns ytx from the binary itself.
//
//go:embed SKILL.md
var Skill string
