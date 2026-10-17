package report

import (
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/engine"
)

const (
	sarifVersion = "2.1.0"
	sarifSchema  = "https://json.schemastore.org/sarif-2.1.0.json"
	// srcRoot is the uriBaseId every artifact location is relative to.
	srcRoot = "%SRCROOT%"
	// fingerprintKey names narc's fingerprint in partialFingerprints. It is
	// stable when code moves between lines, so alerts survive rebases.
	fingerprintKey = "narcFingerprint/v1"
)

var semverRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?$`)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool               sarifTool                        `json:"tool"`
	Invocations        []sarifInvocation                `json:"invocations"`
	OriginalURIBaseIDs map[string]sarifArtifactLocation `json:"originalUriBaseIds,omitempty"`
	Results            []sarifResult                    `json:"results"`
	Properties         sarifRunProps                    `json:"properties"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name            string      `json:"name"`
	Version         string      `json:"version"`
	SemanticVersion string      `json:"semanticVersion,omitempty"`
	InformationURI  string      `json:"informationUri"`
	Rules           []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	ShortDescription     sarifMessage    `json:"shortDescription"`
	FullDescription      sarifMessage    `json:"fullDescription"`
	Help                 sarifHelp       `json:"help"`
	HelpURI              string          `json:"helpUri"`
	DefaultConfiguration sarifRuleConfig `json:"defaultConfiguration"`
	Properties           sarifRuleProps  `json:"properties"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifHelp struct {
	Text     string `json:"text"`
	Markdown string `json:"markdown"`
}

type sarifRuleConfig struct {
	Level string `json:"level"`
}

// sarifRuleProps carries the property-bag fields GitHub code scanning
// reads: tags for filtering, precision and problem.severity for ranking.
type sarifRuleProps struct {
	Tags            []string `json:"tags"`
	Precision       string   `json:"precision"`
	ProblemSeverity string   `json:"problem.severity"`
}

type sarifResult struct {
	RuleID              string             `json:"ruleId"`
	RuleIndex           int                `json:"ruleIndex"`
	Level               string             `json:"level"`
	Message             sarifMessage       `json:"message"`
	Locations           []sarifLocation    `json:"locations"`
	PartialFingerprints map[string]string  `json:"partialFingerprints"`
	Suppressions        []sarifSuppression `json:"suppressions,omitempty"`
	Properties          sarifResultProps   `json:"properties"`
}

type sarifResultProps struct {
	Category string `json:"category,omitempty"`
	OldLine  int    `json:"oldLine,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

type sarifSuppression struct {
	Kind          string `json:"kind"` // inSource or external
	Status        string `json:"status"`
	Justification string `json:"justification"`
}

type sarifInvocation struct {
	ExecutionSuccessful        bool                `json:"executionSuccessful"`
	CommandLine                string              `json:"commandLine,omitempty"`
	ToolExecutionNotifications []sarifNotification `json:"toolExecutionNotifications,omitempty"`
}

type sarifNotification struct {
	Level   string       `json:"level"`
	Message sarifMessage `json:"message"`
}

type sarifRunProps struct {
	Label   string `json:"label,omitempty"`
	Verdict string `json:"verdict"`
	Score   int    `json:"score"`
}

// SARIF writes a SARIF 2.1.0 log suitable for GitHub code scanning
// (github/codeql-action/upload-sarif). rules become the driver's rule
// descriptors, normally core.Rules(); rules referenced by findings but not
// passed in are described from the findings themselves.
//
// Every result has a physical location relative to %SRCROOT%; file-level
// findings point at line 1. Waived findings are included with a
// suppression ("inSource" for narc:allow comments, "external" for allow:
// entries in .narc.yml) whose justification is the waiver reason, so code
// scanning shows them as dismissed rather than dropping them.
func SARIF(w io.Writer, res *engine.Result, rules []*core.Rule, o Options) error {
	if res == nil {
		return errNilResult
	}
	v := newView(res)
	set, index := sarifRuleSet(rules, v.all)
	descriptors := make([]sarifRule, len(set))
	for i, r := range set {
		descriptors[i] = sarifRuleFor(r)
	}

	run := sarifRun{
		Tool: sarifTool{Driver: sarifDriver{
			Name:           "narc",
			Version:        o.version(),
			InformationURI: ProjectURL,
			Rules:          descriptors,
		}},
		Invocations: []sarifInvocation{{ExecutionSuccessful: true, CommandLine: o.Command}},
		Results:     make([]sarifResult, 0, len(v.all)),
		Properties:  sarifRunProps{Label: res.Label, Verdict: v.verdict.key, Score: v.score},
	}
	if semverRe.MatchString(o.version()) {
		run.Tool.Driver.SemanticVersion = strings.TrimPrefix(o.version(), "v")
	}
	if root := rootURI(o.Root); root != "" {
		run.OriginalURIBaseIDs = map[string]sarifArtifactLocation{srcRoot: {URI: root}}
	}
	for _, e := range res.Errors {
		run.Invocations[0].ToolExecutionNotifications = append(run.Invocations[0].ToolExecutionNotifications,
			sarifNotification{Level: "error", Message: sarifMessage{Text: firstLine(e)}})
	}

	// Fingerprints ignore line numbers, so that alerts survive code moving.
	// The same change made twice in one file (two identical `as any`
	// casts) therefore fingerprints the same, and code scanning would merge
	// the two into one alert. Later occurrences get an occurrence suffix,
	// the way github/codeql-action numbers its own line hashes.
	occurrences := map[string]int{}
	for _, f := range v.all {
		i := index[f.RuleID]
		fp := fingerprint(f)
		occurrences[fp]++
		if n := occurrences[fp]; n > 1 {
			fp += fmt.Sprintf(":%d", n)
		}
		r := sarifResult{
			RuleID:    f.RuleID,
			RuleIndex: i,
			Level:     sarifLevel(f.Severity),
			Message:   sarifMessage{Text: sarifResultText(f, set[i].Hint)},
			Locations: []sarifLocation{{PhysicalLocation: sarifPhysicalLocation{
				ArtifactLocation: sarifArtifactLocation{URI: artifactURI(f.Path, o.Root), URIBaseID: srcRoot},
				// File-level findings (line 0) still need a valid region.
				Region: sarifRegion{StartLine: max(f.Line, 1)},
			}}},
			PartialFingerprints: map[string]string{fingerprintKey: fp},
			Properties:          sarifResultProps{Category: f.Category, OldLine: f.OldLine},
		}
		if f.Waived {
			kind := "inSource"
			switch {
			case f.WaiverSource == "config":
				kind = "external"
			case f.WaiverSource == "" && waivedByConfig(f, o.Config):
				kind = "external"
			}
			r.Suppressions = []sarifSuppression{{Kind: kind, Status: "accepted", Justification: f.WaiverReason}}
		}
		run.Results = append(run.Results, r)
	}

	return encodeJSON(w, sarifLog{Schema: sarifSchema, Version: sarifVersion, Runs: []sarifRun{run}})
}

// sarifRuleSet returns the rules to describe and each rule ID's index.
// Rules that findings reference but that were not passed in are appended,
// described from the first finding that uses them.
func sarifRuleSet(rules []*core.Rule, fs []core.Finding) ([]*core.Rule, map[string]int) {
	var out []*core.Rule
	index := map[string]int{}
	add := func(r *core.Rule) {
		index[r.ID] = len(out)
		out = append(out, r)
	}
	for _, r := range rules {
		if r == nil || r.ID == "" {
			continue
		}
		if _, dup := index[r.ID]; !dup {
			add(r)
		}
	}
	var missing []*core.Rule
	seen := map[string]bool{}
	for _, f := range fs {
		if _, ok := index[f.RuleID]; ok || seen[f.RuleID] {
			continue
		}
		seen[f.RuleID] = true
		missing = append(missing, &core.Rule{ID: f.RuleID, Title: f.Title, Category: f.Category, Severity: f.Severity, Hint: f.Hint})
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].ID < missing[j].ID })
	for _, r := range missing {
		add(r)
	}
	return out, index
}

func sarifRuleFor(r *core.Rule) sarifRule {
	title := firstNonEmpty(r.Title, r.ID)
	summary := firstNonEmpty(r.Summary, title)
	text := summary
	if r.Hint != "" {
		text += "\n\nHow to fix: " + r.Hint
	}
	tags := []string{"narc"}
	if r.Category != "" {
		tags = append(tags, r.Category)
	}
	return sarifRule{
		ID:                   r.ID,
		Name:                 pascalCase(r.ID),
		ShortDescription:     sarifMessage{Text: title},
		FullDescription:      sarifMessage{Text: summary},
		Help:                 sarifHelp{Text: text, Markdown: ruleHelpMarkdown(r)},
		HelpURI:              RuleDocURL(r.ID),
		DefaultConfiguration: sarifRuleConfig{Level: sarifLevel(r.Severity)},
		Properties: sarifRuleProps{
			Tags:            tags,
			Precision:       sarifPrecision(r.Severity),
			ProblemSeverity: sarifProblemSeverity(r.Severity),
		},
	}
}

// ruleHelpMarkdown is the rule help code scanning shows next to an alert.
func ruleHelpMarkdown(r *core.Rule) string {
	var b strings.Builder
	b.WriteString(firstNonEmpty(r.Summary, r.Title, r.ID))
	if d := strings.TrimSpace(r.Docs); d != "" {
		b.WriteString("\n\n" + d)
	}
	if r.Hint != "" {
		b.WriteString("\n\n**How to fix:** " + r.Hint)
	}
	if r.ID != engine.WaiverRuleID {
		fmt.Fprintf(&b, "\n\nIf the change is intentional, waive it with a reason: `narc:allow %s reason=\"...\"` "+
			"in a comment on or above the line, or an `allow:` entry in `.narc.yml`.", r.ID)
	}
	fmt.Fprintf(&b, "\n\n[Rule reference](%s)", RuleDocURL(r.ID))
	return b.String()
}

// sarifResultText is the alert message: what happened, the code it
// replaced or removed (which is no longer visible in the file), and the
// hint when it says more than the rule's default advice.
func sarifResultText(f core.Finding, ruleHint string) string {
	text := firstNonEmpty(f.Message, f.Title, f.RuleID)
	if f.Before != "" {
		label := "Removed"
		if f.Evidence != "" {
			label = "Before"
		}
		text += "\n" + label + ": " + oneLine(f.Before)
	}
	if f.Hint != "" && f.Hint != ruleHint {
		text += "\nHint: " + f.Hint
	}
	// Code scanning shows this to people: make control characters and
	// bidi overrides from the source visible, as the other formats do.
	return sanitize(text, true)
}

func sarifLevel(s core.Severity) string {
	switch s {
	case core.Error:
		return "error"
	case core.Warning:
		return "warning"
	}
	return "note"
}

func sarifPrecision(s core.Severity) string {
	switch s {
	case core.Error:
		return "high"
	case core.Warning:
		return "medium"
	}
	return "low"
}

func sarifProblemSeverity(s core.Severity) string {
	switch s {
	case core.Error:
		return "error"
	case core.Warning:
		return "warning"
	}
	return "recommendation"
}

// artifactURI turns a finding path into a relative URI reference. Paths
// are repository-relative already; an absolute path under root is made
// relative to it.
func artifactURI(p, root string) string {
	p = filepath.ToSlash(p)
	if root != "" {
		r := strings.TrimSuffix(filepath.ToSlash(root), "/") + "/"
		p = strings.TrimPrefix(p, r)
	}
	p = strings.TrimPrefix(p, "./")
	return (&url.URL{Path: p}).String()
}

// rootURI is the file:// URI of the repository root, ending in a slash as
// SARIF requires for base URIs. It is "" when root is unknown.
func rootURI(root string) string {
	if root == "" {
		return ""
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	p := filepath.ToSlash(root)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive letters: file:///C:/repo/
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// pascalCase turns a rule ID into a SARIF rule name: skip-added → SkipAdded.
func pascalCase(id string) string {
	var b strings.Builder
	upper := true
	for _, r := range id {
		if r == '-' || r == '_' || r == ' ' {
			upper = true
			continue
		}
		if upper {
			r = unicode.ToUpper(r)
			upper = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// maxOneLineBytes caps code quoted in an annotation or alert message: a
// line removed from a snapshot or a minified file can be hundreds of
// kilobytes. JSON keeps the full text.
const maxOneLineBytes = 500

// oneLine joins multi-line code with " ⏎ " for one-line contexts, capped
// at maxOneLineBytes.
func oneLine(s string) string {
	return clipBytes(strings.Join(codeSplit(s), " ⏎ "), maxOneLineBytes, "…")
}
