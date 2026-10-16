package integrity_test

import (
	"testing"

	_ "github.com/huyth45/narc/internal/rules/integrity"
	"github.com/huyth45/narc/internal/ruletest"
)

func TestCorpus(t *testing.T) { ruletest.RunDir(t, "testdata") }
