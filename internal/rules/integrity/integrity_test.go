package integrity_test

import (
	"testing"

	_ "github.com/tranhuuhuy297/narc/internal/rules/integrity"
	"github.com/tranhuuhuy297/narc/internal/ruletest"
)

func TestCorpus(t *testing.T) { ruletest.RunDir(t, "testdata") }
