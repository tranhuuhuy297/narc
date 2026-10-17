package escapes

import (
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// readOnlySource serves contents but cannot list the repository, like the
// sources agent hooks build around a single edit.
type readOnlySource struct{ files map[string]string }

func (s readOnlySource) Old(p string) ([]byte, bool) { b, ok := s.files[p]; return []byte(b), ok }
func (s readOnlySource) New(p string) ([]byte, bool) { b, ok := s.files[p]; return []byte(b), ok }

func TestRelatedTests(t *testing.T) {
	repo := map[string]string{
		"src/text/slugify.ts":                         "",
		"src/text/slugify.test.ts":                    "",
		"src/text/__tests__/slugify.ts":               "",
		"packages/b/test/slugify.test.ts":             "",
		"textkit/count.py":                            "",
		"tests/test_count.py":                         "",
		"internal/digest/crc.go":                      "",
		"internal/digest/digest_test.go":              "",
		"internal/digest/sub/other_test.go":           "",
		"src/main/java/com/acme/Roman.java":           "",
		"src/test/java/com/acme/RomanTest.java":       "",
		"src/test/java/com/other/RomanTest.java":      "",
		"Sources/Acme/Parser.swift":                   "",
		"Tests/AcmeTests/ParserTests.swift":           "",
		"app/models/user.rb":                          "",
		"spec/models/user_spec.rb":                    "",
		"src/Acme.Billing/Invoice.cs":                 "",
		"tests/Acme.Billing.Tests/InvoiceTests.cs":    "",
		"tests/Acme.Shipping.Tests/InvoiceTests.cs":   "",
		"lib/acme/parser.ex":                          "",
		"test/acme/parser_test.exs":                   "",
		"src/Billing/Invoice.php":                     "",
		"tests/Unit/Billing/InvoiceTest.php":          "",
		"tests/fixtures/count/test_count.py.snapshot": "",
	}
	want := map[string]string{
		"src/text/slugify.ts":               "src/text/__tests__/slugify.ts src/text/slugify.test.ts",
		"textkit/count.py":                  "tests/test_count.py",
		"internal/digest/crc.go":            "internal/digest/digest_test.go",
		"src/main/java/com/acme/Roman.java": "src/test/java/com/acme/RomanTest.java",
		"Sources/Acme/Parser.swift":         "Tests/AcmeTests/ParserTests.swift",
		"app/models/user.rb":                "spec/models/user_spec.rb",
		"src/Acme.Billing/Invoice.cs":       "tests/Acme.Billing.Tests/InvoiceTests.cs",
		"lib/acme/parser.ex":                "test/acme/parser_test.exs",
		"src/Billing/Invoice.php":           "tests/Unit/Billing/InvoiceTest.php",
	}
	// Without a listing, conventional locations are probed: Go tests in
	// other files of the package and C#/Swift sibling projects are missed.
	probed := map[string]string{
		"src/text/slugify.ts":               "src/text/__tests__/slugify.ts src/text/slugify.test.ts",
		"textkit/count.py":                  "tests/test_count.py",
		"internal/digest/crc.go":            "",
		"src/main/java/com/acme/Roman.java": "src/test/java/com/acme/RomanTest.java",
		"app/models/user.rb":                "spec/models/user_spec.rb",
		"lib/acme/parser.ex":                "test/acme/parser_test.exs",
		"src/Billing/Invoice.php":           "tests/Unit/Billing/InvoiceTest.php",
	}
	mem := &core.MemSource{NewFiles: map[string][]byte{}}
	for p, c := range repo {
		mem.NewFiles[p] = []byte(c)
	}
	for src, w := range want {
		f := &core.FileInfo{Path: src, Diff: &diff.FileDiff{NewPath: src, OldPath: src}}
		f.Lang = lang.Detect(src)
		a := &core.Analysis{Source: mem}
		if got := strings.Join(relatedTests(a, f), " "); got != w {
			t.Errorf("listed: relatedTests(%s) = %q, want %q", src, got, w)
		}
	}
	for src, w := range probed {
		f := &core.FileInfo{Path: src, Diff: &diff.FileDiff{NewPath: src, OldPath: src}}
		f.Lang = lang.Detect(src)
		a := &core.Analysis{Source: readOnlySource{repo}}
		if got := strings.Join(relatedTests(a, f), " "); got != w {
			t.Errorf("probed: relatedTests(%s) = %q, want %q", src, got, w)
		}
	}
}
