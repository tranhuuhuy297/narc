package audit

import (
	"regexp"
	"strings"
)

// Attribution says who (or what) wrote a commit.
type Attribution struct {
	Agent      string `json:"agent,omitempty"` // canonical agent name; "" for humans
	Model      string `json:"model,omitempty"` // model name when the trailer carries it
	Signal     string `json:"signal,omitempty"`
	Strength   string `json:"strength,omitempty"`   // strong, medium, weak
	Automation bool   `json:"automation,omitempty"` // dependency/release bots: excluded from stats
}

// IsAgent reports whether the commit was attributed to an AI agent.
func (a Attribution) IsAgent() bool { return a.Agent != "" }

type signalKind int

const (
	kAuthorName    signalKind = iota // author name
	kAuthorEmail                     // author email
	kCommitterName                   // committer name
	kTrailer                         // "Co-authored-by" etc. — matched against "Key: value"
	kMessage                         // anywhere in the message body
	kSubjectPrefix                   // start of the subject line
)

type signature struct {
	agent string
	// dynamicAgent derives the agent name from the match when agent is "".
	dynamicAgent func(match []string) string
	kind         signalKind
	re           *regexp.Regexp
	strength     string
	// model extracts a model name from the matched text, if any.
	model func(match []string) string
}

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// automationRe matches bots that are not AI coding agents. Their commits
// are left out of both the agent and the human statistics.
var automationRe = re(`(?i)^(dependabot(-preview)?|renovate|renovate-bot|github-actions|pre-commit-ci|mergify|greenkeeper|snyk-bot|imgbot|allcontributors|semantic-release-bot|release-please|changeset-bot|web-flow|copybara-service|googlebot)(\[bot\])?$`)

// signatures is ordered: the first match wins, so stronger and more
// specific signals come first. Patterns were verified against real commits
// in public repositories (Oct 2026). Trailer patterns are matched against
// "Key: value" lines; message patterns against the whole body.
//
// Deliberately NOT attributed: trailers that mean a human accepted an AI
// review suggestion (Copilot Autofix, Gemini Code Assist suggestions).
var signatures = []signature{
	// --- Claude Code (CLI, desktop, IDE, web) and the Claude GitHub app ---
	{agent: "Claude Code", kind: kTrailer, strength: "strong",
		re:    re(`(?i)^co-authored-by:[ \t]*(claude(?:[ \t]+[^<]*?)?)[ \t]*<noreply@anthropic\.com>`),
		model: claudeModel},
	{agent: "Claude Code", kind: kTrailer, strength: "strong", re: re(`(?i)^claude-session:[ \t]*https://claude\.ai/code/session_`)},
	{agent: "Claude Code", kind: kMessage, strength: "strong", re: re(`(?i)generated with \[?claude code\]?`)},
	{agent: "Claude Code", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(noreply@anthropic\.com|(\d+\+)?claude\[bot\]@users\.noreply\.github\.com)$`)},
	{agent: "Claude Code", kind: kAuthorName, strength: "strong", re: re(`^claude\[bot\]$`)},
	{agent: "Claude Code", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(\d+\+)?claude\[bot\]@users\.noreply\.github\.com>`)},

	// --- OpenAI Codex (CLI, IDE, app, cloud) ---
	{agent: "Codex", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[ \t]*(openai[ \t]+)?codex[ \t]*<(noreply|codex)@openai\.com>`)},
	{agent: "Codex", kind: kMessage, strength: "strong", re: re(`(?i)generated with \[codex\]\(https://(openai\.com/codex|chatgpt\.com/codex)`)},
	{agent: "Codex", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?chatgpt-codex-connector\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Codex", kind: kAuthorName, strength: "strong", re: re(`^chatgpt-codex-connector\[bot\]$`)},
	{agent: "Codex", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(\d+\+)?chatgpt-codex-connector\[bot\]@users\.noreply\.github\.com>`)},

	// --- GitHub Copilot: coding agent, CLI/app, IDE ---
	{agent: "Copilot", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(198982749|223556219)\+copilot@users\.noreply\.github\.com$`)},
	{agent: "Copilot", kind: kAuthorName, strength: "strong", re: re(`^copilot-swe-agent\[bot\]$`)},
	{agent: "Copilot", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(198982749|223556219)\+copilot@users\.noreply\.github\.com>`)},
	{agent: "Copilot", kind: kTrailer, strength: "strong", re: re(`(?i)^(agent-logs-url:[ \t]*https://github\.com/[^/\s]+/[^/\s]+/sessions/|copilot-session:)`)},
	{agent: "Copilot (IDE)", kind: kTrailer, strength: "medium", re: re(`(?i)^co-authored-by:[ \t]*copilot[ \t]*<copilot@github\.com>`)},

	// --- Cursor (cloud/background agents, IDE & CLI agent, Bugbot) ---
	{agent: "Cursor", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(cursoragent@cursor\.com|(\d+\+)?cursor\[bot\]@users\.noreply\.github\.com)$`)},
	{agent: "Cursor", kind: kTrailer, strength: "strong",
		re:    re(`(?i)^co-authored-by:[ \t]*cursor(?:[ \t]+([^<]*?))?[ \t]*<cursoragent@cursor\.com>`),
		model: cursorModel},
	{agent: "Cursor", kind: kTrailer, strength: "strong", re: re(`(?i)^made-with:[ \t]*cursor[ \t]*$`)},
	{agent: "Cursor", kind: kMessage, strength: "medium", re: re(`(?i)made with \[cursor\]\(https://cursor\.com\)`)},

	// --- Devin ---
	{agent: "Devin", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?devin-ai-integration\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Devin", kind: kAuthorName, strength: "strong", re: re(`^devin-ai-integration\[bot\]$`)},
	{agent: "Devin", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(\d+\+)?devin-ai-integration\[bot\]@users\.noreply\.github\.com>`)},
	{agent: "Devin", kind: kMessage, strength: "medium", re: re(`(?i)(https://app\.devin\.ai/sessions/[0-9a-f]{32}|generated with \[devin\]\(https://devin\.ai\))`)},

	// --- Google: Jules, Gemini CLI, Antigravity ---
	{agent: "Jules", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?google-labs-jules\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Jules", kind: kAuthorName, strength: "strong", re: re(`^google-labs-jules\[bot\]$`)},
	{agent: "Jules", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(\d+\+)?google-labs-jules\[bot\]@users\.noreply\.github\.com>`)},
	{agent: "Antigravity", kind: kTrailer, strength: "medium", re: re(`(?i)^co-authored-by:[^<]*<\d+\+google-antigravity@users\.noreply\.github\.com>`)},
	{agent: "Gemini CLI", kind: kTrailer, strength: "weak", re: re(`(?i)^co-authored-by:[ \t]*gemini(?:[ -]cli)?(?:[ \t][^<]*)?[ \t]*<`)},

	// --- Aider ---
	{agent: "Aider", kind: kTrailer, strength: "strong",
		re:    re(`(?i)^co-authored-by:[ \t]*aider[ \t]*\(([^)]+)\)[ \t]*<[^>]*aider\.chat>`),
		model: func(m []string) string { return strings.TrimSpace(m[1]) }},
	{agent: "Aider", kind: kAuthorName, strength: "strong", re: re(`[ \t]\(aider\)$`)},
	{agent: "Aider", kind: kCommitterName, strength: "medium", re: re(`[ \t]\(aider\)$`)},
	{agent: "Aider", kind: kSubjectPrefix, strength: "strong", re: re(`^aider: `)},

	// --- OpenCode, Kilo Code ---
	{agent: "OpenCode", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?opencode-agent\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "OpenCode", kind: kAuthorName, strength: "strong", re: re(`^opencode-agent\[bot\]$`)},
	{agent: "OpenCode", kind: kTrailer, strength: "medium", re: re(`(?i)^co-authored-by:[ \t]*opencode[ \t]*<(noreply@opencode\.ai|opencode@sst\.dev)>`)},
	{agent: "OpenCode", kind: kMessage, strength: "medium", re: re(`(?i)generated with \[?opencode\]?(\(https://opencode\.ai\))?`)},
	{agent: "Kilo Code", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?(kilo-code-bot|kiloconnect)\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Kilo Code", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<((\d+\+)?(kilo-code-bot|kiloconnect)\[bot\]@users\.noreply\.github\.com|noreply@kilocode\.ai)>`)},

	// --- Amp ---
	{agent: "Amp", kind: kTrailer, strength: "strong", re: re(`(?i)^(amp-thread(-id)?:[ \t]*https://ampcode\.com/threads/|co-authored-by:[ \t]*amp[ \t]*<amp@ampcode\.com>)`)},
	{agent: "Amp", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^amp@ampcode\.com$`)},

	// --- Other agents and cloud-agent apps ---
	{agent: "Factory Droid", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?factory-droid\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Factory Droid", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<((\d+\+)?factory-droid\[bot\]@users\.noreply\.github\.com|droid@factory\.ai)>`)},
	{agent: "Kiro", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?kiro-agent(\[bot\])?@users\.noreply\.github\.com$`)},
	{agent: "Kiro", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(\d+\+)?kiro-agent(\[bot\])?@users\.noreply\.github\.com>`)},
	{agent: "Replit Agent", kind: kTrailer, strength: "strong", re: re(`(?i)^replit-commit-author:[ \t]*(agent|assistant)[ \t]*$`)},
	{agent: "Replit Agent", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^agent@replit\.com$`)},
	{agent: "OpenHands", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(openhands|opendevin)@all-hands\.dev$`)},
	{agent: "OpenHands", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(openhands@all-hands\.dev|(\d+\+)?openhands-agent(\[bot\])?@users\.noreply\.github\.com)>`)},
	{agent: "Qwen Code", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[ \t]*qwen-coder[ \t]*<`)},
	{agent: "Qwen Code", kind: kMessage, strength: "strong", re: re(`(?i)generated with \[?qwen code\]?`)},
	{agent: "Crush", kind: kTrailer, strength: "strong",
		re:    re(`(?i)^assisted-by:[ \t]*crush:[ \t]*(\S+)`),
		model: func(m []string) string { return m[1] }},
	{agent: "Crush", kind: kTrailer, strength: "strong", re: re(`(?i)^(assisted-by|co-authored-by):[^<]*<crush@charm\.land>`)},
	{agent: "Crush", kind: kMessage, strength: "strong", re: re(`(?i)generated with crush`)},
	{agent: "Roo Code", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?roomote\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Roo Code", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(roomote@roocode\.com|(\d+\+)?roomote\[bot\]@users\.noreply\.github\.com)>`)},
	{agent: "Warp", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(agent|oz-agent)@warp\.dev$`)},
	{agent: "Warp", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[^<]*<(agent|oz-agent)@warp\.dev>`)},
	{agent: "Continue", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[ \t]*continue[ \t]*<noreply@continue\.dev>`)},
	{agent: "Continue", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?continue\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Junie", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[ \t]*junie[ \t]*<junie@jetbrains\.com>`)},
	{agent: "Junie", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?jetbrains-junie\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Mistral Vibe", kind: kTrailer, strength: "strong", re: re(`(?i)^co-authored-by:[ \t]*mistral vibe[ \t]*<vibe@mistral\.ai>`)},
	{agent: "Augment", kind: kTrailer, strength: "medium", re: re(`(?i)^co-authored-by:[^<]*<noreply@augmentcode\.com>`)},
	{agent: "Sweep", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?sweep-ai(-deprecated)?\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Codegen", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?codegen-sh\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Lovable", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?(gpt-engineer-app|lovable-dev)\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Amazon Q", kind: kAuthorEmail, strength: "strong", re: re(`(?i)^(\d+\+)?amazon-q-developer\[bot\]@users\.noreply\.github\.com$`)},
	{agent: "Cline", kind: kTrailer, strength: "weak", re: re(`(?i)^co-authored-by:[ \t]*cline[ \t]*<(noreply@cline\.bot|(\d+\+)?cline\[bot\]@users\.noreply\.github\.com)>`)},
	{agent: "Windsurf", kind: kTrailer, strength: "weak", re: re(`(?i)^co-authored-by:[ \t]*(cascade|windsurf)[^<]*<[^>]*@codeium\.com>`)},
	{agent: "Grok", kind: kTrailer, strength: "medium", re: re(`(?i)^co-authored-by:[ \t]*grok[ \t]*<grok@x\.ai>`)},
	{agent: "TRAE", kind: kTrailer, strength: "medium", re: re(`(?i)^co-authored-by:[ \t]*trae(?:[ \t]+cli)?[ \t]*<traecli@bytedance\.com>`)},
	{agent: "Goose", kind: kTrailer, strength: "weak", re: re(`(?i)^co-authored-by:[ \t]*goose[ \t]*<opensource@block\.xyz>`)},

	// --- Wrappers and generic disclosures (last: least specific) ---
	{agent: "", kind: kTrailer, strength: "strong",
		re:    re(`(?i)^entire-agent:[ \t]*(.+)$`),
		model: nil, dynamicAgent: func(m []string) string { return strings.TrimSpace(m[1]) }},
	{agent: "", kind: kTrailer, strength: "medium",
		re:           re(`(?i)^assisted-by:[ \t]*([^:<]+?)(?::[ \t]*([^\s<]+))?[ \t]*(<[^>]*>)?$`),
		dynamicAgent: assistedAgent,
		model:        func(m []string) string { return strings.TrimSpace(m[2]) }},
}

var parenRe = regexp.MustCompile(`\s*\([^)]*\)`)

// claudeModel extracts "Opus 4.5" from "Claude Opus 4.5 (1M context)".
func claudeModel(m []string) string {
	name := strings.TrimSpace(parenRe.ReplaceAllString(m[1], ""))
	if len(name) <= len("claude") {
		return ""
	}
	return strings.TrimSpace(name[len("claude"):])
}

// cursorModel extracts a model from "Cursor <model>", ignoring "Cursor Agent".
func cursorModel(m []string) string {
	v := strings.TrimSpace(m[1])
	if strings.EqualFold(v, "agent") {
		return ""
	}
	return v
}

// assistedAgent maps the free-form agent in an "Assisted-by:" trailer
// (kernel convention: "Assisted-by: AGENT:MODEL") to a display name.
func assistedAgent(m []string) string {
	name := strings.TrimSpace(m[1])
	l := strings.ToLower(name)
	switch {
	case name == "":
		return ""
	case strings.HasPrefix(l, "claude"):
		return "Claude Code"
	case strings.HasPrefix(l, "codex"), strings.HasPrefix(l, "gpt"), strings.HasPrefix(l, "openai"):
		return "Codex"
	case strings.HasPrefix(l, "copilot"):
		return "Copilot"
	case strings.HasPrefix(l, "cursor"):
		return "Cursor"
	case strings.HasPrefix(l, "gemini"):
		return "Gemini CLI"
	}
	if len(name) > 24 {
		name = name[:24]
	}
	return name
}

// Attribute decides who wrote a commit.
func Attribute(c *Commit) Attribution {
	authorLocal := strings.TrimSpace(c.AuthorName)
	if automationRe.MatchString(authorLocal) {
		return Attribution{Automation: true, Signal: "automation bot " + authorLocal, Strength: "strong"}
	}
	for _, s := range signatures {
		var subject string
		var m []string
		switch s.kind {
		case kAuthorName:
			subject = c.AuthorName + " <" + c.AuthorEmail + ">"
			m = s.re.FindStringSubmatch(strings.TrimSpace(c.AuthorName))
		case kAuthorEmail:
			subject = c.AuthorName + " <" + c.AuthorEmail + ">"
			m = s.re.FindStringSubmatch(strings.TrimSpace(c.AuthorEmail))
		case kCommitterName:
			subject = c.CommitterName + " <" + c.CommitterEmail + ">"
			m = s.re.FindStringSubmatch(strings.TrimSpace(c.CommitterName))
		case kTrailer:
			for _, t := range c.Trailers {
				line := t.Key + ": " + t.Value
				if m = s.re.FindStringSubmatch(line); m != nil {
					subject = line
					break
				}
			}
		case kMessage:
			subject = c.Body
			m = s.re.FindStringSubmatch(c.Body)
		case kSubjectPrefix:
			subject = c.Subject
			m = s.re.FindStringSubmatch(c.Subject)
		}
		if m == nil {
			continue
		}
		agent := s.agent
		if agent == "" && s.dynamicAgent != nil {
			agent = s.dynamicAgent(m)
		}
		if agent == "" {
			continue
		}
		a := Attribution{Agent: agent, Signal: describeSignal(s.kind, subject), Strength: s.strength}
		if s.model != nil {
			a.Model = s.model(m)
		}
		if a.Model == "" {
			a.Model = modelFromTrailers(s.agent, c)
		}
		return a
	}
	return Attribution{}
}

// modelFromTrailers finds a model name in a later Claude trailer when the
// winning signal did not carry one (e.g. footer matched first).
func modelFromTrailers(agent string, c *Commit) string {
	if agent != "Claude Code" {
		return ""
	}
	r := signatures[0].re
	for _, t := range c.Trailers {
		if m := r.FindStringSubmatch(t.Key + ": " + t.Value); m != nil {
			return claudeModel(m)
		}
	}
	return ""
}

func describeSignal(k signalKind, s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	switch k {
	case kAuthorName, kAuthorEmail:
		return "author " + s
	case kCommitterName:
		return "committer " + s
	case kTrailer:
		return "trailer " + s
	case kSubjectPrefix:
		return "subject " + s
	}
	return "message"
}
