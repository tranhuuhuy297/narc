// Package all registers every built-in narc rule.
package all

import (
	_ "github.com/tranhuuhuy297/narc/internal/rules/accounting"
	_ "github.com/tranhuuhuy297/narc/internal/rules/ci"
	_ "github.com/tranhuuhuy297/narc/internal/rules/escapes"
	_ "github.com/tranhuuhuy297/narc/internal/rules/integrity"
	_ "github.com/tranhuuhuy297/narc/internal/rules/tests"
)
