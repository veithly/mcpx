package security

import (
	"regexp"
	"strings"

	"mcpx/internal/config"
)

// Decision is the outcome of command policy matching.
type Decision int

const (
	// Allow executes without human confirmation.
	Allow Decision = iota
	// Confirm requires explicit user semantic confirmation before execute.
	Confirm
	// Deny rejects the command.
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Confirm:
		return "confirm"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// ParseDefault maps config string to Decision. Unknown/empty => Confirm (safe built-in).
func ParseDefault(s string) Decision {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "deny":
		return Deny
	case "confirm", "":
		return Confirm
	default:
		return Confirm
	}
}

// CommandSegmentDecision is one independently reviewed shell segment. Operator
// is the control operator that follows this segment (&&, ||, ;, |, newline), or empty for
// the final segment.
type CommandSegmentDecision struct {
	Command  string
	Operator string
	Decision Decision
}

// CommandAnalysis is the complete preflight result for one shell command.
// Execution is allowed only after every segment has been reviewed.
type CommandAnalysis struct {
	Decision Decision
	Segments []CommandSegmentDecision
	Unsafe   bool
}

// AnalyzeCommand splits supported compound commands before evaluating policy.
// &&, ||, ;, |, and newlines between simple commands are supported because each
// segment can be judged before the original command is passed to the shell. When
// a command uses shell syntax that cannot be segmented, the deny/confirm/allow
// lists and the default decision are applied to the raw command text instead of
// rejecting it outright; Unsafe records that per-segment preflight was impossible.
func AnalyzeCommand(rules config.CommandRules, command string) CommandAnalysis {
	parsed, unsafe := commandSegments(command)
	if unsafe {
		return CommandAnalysis{Decision: matchUnparsedCommand(rules, command), Unsafe: true}
	}
	if len(parsed) == 0 {
		return CommandAnalysis{Decision: Deny}
	}
	analysis := CommandAnalysis{Decision: Allow}
	analysis.Segments = make([]CommandSegmentDecision, 0, len(parsed))
	for _, segment := range parsed {
		decision := matchSegment(rules, segment.Command)
		analysis.Segments = append(analysis.Segments, CommandSegmentDecision{
			Command: segment.Command, Operator: segment.Operator, Decision: decision,
		})
		if decision == Deny {
			analysis.Decision = Deny
			continue
		}
		if decision == Confirm && analysis.Decision == Allow {
			analysis.Decision = Confirm
		}
	}
	return analysis
}

// MatchCommand returns the aggregate decision after every supported compound
// command segment has been reviewed.
func MatchCommand(rules config.CommandRules, command string) Decision {
	return AnalyzeCommand(rules, command).Decision
}

// matchSegment evaluates a single command segment without control operators.
func matchSegment(rules config.CommandRules, segment string) Decision {
	if matchAny(rules.Deny, segment) {
		return Deny
	}
	if matchAny(rules.Confirm, segment) {
		return Confirm
	}
	if matchAny(rules.Allow, segment) {
		return Allow
	}
	if autoAllowReadonlyEnabled(rules) && isReadonlyCommand(segment) {
		return Allow
	}
	decision := ParseDefault(rules.Default)
	return decision
}

// matchUnparsedCommand evaluates policy against the raw command text when the
// shell syntax cannot be split into independently audited segments. List order
// and the default fallback match matchSegment.
func matchUnparsedCommand(rules config.CommandRules, command string) Decision {
	if matchAny(rules.Deny, command) {
		return Deny
	}
	if matchAny(rules.Confirm, command) {
		return Confirm
	}
	if matchAny(rules.Allow, command) {
		return Allow
	}
	return ParseDefault(rules.Default)
}

// HasUnsafeShellOperator reports active shell syntax that cannot be safely
// preflighted. &&, ||, ;, |, and newlines between simple commands are supported
// separators: every stage is judged independently. Quoted heredocs are literal
// stdin; commands following their terminators are audited as separate segments.
// Redirections that cannot change a segment's behavior (/dev/null sinks, fd
// duplication, stdin from /dev/null) are skipped by the scanner. Background
// operators, file-writing redirections, unquoted heredocs, complex multiline
// shell (including comments and dynamic command names), and command substitution
// are reported as unsafe: AnalyzeCommand then applies the policy to the raw
// command text instead of per-segment audit.
// Operators inside quotes or escaped with a backslash are literal content.
func HasUnsafeShellOperator(command string) bool {
	_, unsafe := commandSegments(command)
	return unsafe
}

type commandSegment struct {
	Command  string
	Operator string
}

// commandSegments scans shell control syntax without trying to fully parse a
// shell language. It distinguishes active operators from quoted/escaped
// literals and preserves supported separators for auditing. Multiline input is
// restricted to simple commands with literal command names, not shell programs.
func commandSegments(command string) ([]commandSegment, bool) {
	segments := make([]commandSegment, 0, 2)
	start := 0
	multiline := strings.Contains(command, "\n")
	var quote byte
	escaped := false
	appendSegment := func(end int, operator string) bool {
		segment := strings.TrimSpace(command[start:end])
		if segment == "" || (multiline && !simpleMultilineCommandHead(segment)) {
			return false
		}
		segments = append(segments, commandSegment{Command: segment, Operator: operator})
		return true
	}

	for index := 0; index < len(command); index++ {
		current := command[index]
		if escaped {
			escaped = false
			continue
		}

		switch quote {
		case '\'':
			if current == '\'' {
				quote = 0
			}
			continue
		case '"':
			switch current {
			case '\\':
				escaped = true
			case '"':
				quote = 0
			case '`':
				return nil, true
			case '$':
				if index+1 < len(command) && command[index+1] == '(' {
					return nil, true
				}
			}
			continue
		}

		switch current {
		case '\\':
			escaped = true
		case '\'', '"':
			quote = current
		case '|':
			if index+1 < len(command) && command[index+1] == '|' {
				if !appendSegment(index, "||") {
					return nil, true
				}
				index++
				start = index + 1
				continue
			}
			if !appendSegment(index, "|") {
				return nil, true
			}
			start = index + 1
		case '<':
			if strings.HasPrefix(command[index:], "</dev/null") {
				index += len("</dev/null") - 1
				continue
			}
			if end, ok := quotedHeredocCommandEnd(command, index); ok {
				if !appendSegment(end, "\n") {
					return nil, true
				}
				start = end
				index = end - 1
				continue
			}
			return nil, true
		case '>':
			if skip := benignRedirectSkip(command, index); skip > 0 {
				index += skip - 1
				continue
			}
			return nil, true
		case '\n':
			// Blank lines and line breaks following &&, ||, |, or ; do not
			// introduce another command or erase a pending control operator.
			if strings.TrimSpace(command[start:index]) != "" && !appendSegment(index, "\n") {
				return nil, true
			}
			start = index + 1
		case '#':
			// Do not interpret quotes or operators inside shell comments as
			// syntax that could conceal a later executable line.
			if multiline && (index == start || command[index-1] == ' ' || command[index-1] == '\t') {
				return nil, true
			}
		case '`', '\r':
			return nil, true
		case '$':
			if index+1 < len(command) && command[index+1] == '(' {
				return nil, true
			}
		case '&':
			if index+1 < len(command) && command[index+1] == '&' {
				if !appendSegment(index, "&&") {
					return nil, true
				}
				index++
				start = index + 1
				continue
			}
			return nil, true
		case ';':
			if !appendSegment(index, ";") {
				return nil, true
			}
			start = index + 1
		}
	}
	if quote != 0 || escaped {
		return nil, true
	}
	if strings.TrimSpace(command[start:]) == "" {
		if len(segments) == 0 || segments[len(segments)-1].Operator != "\n" {
			return nil, true
		}
		segments[len(segments)-1].Operator = ""
	} else if !appendSegment(len(command), "") {
		return nil, true
	}
	return segments, false
}

// simpleMultilineCommandHead keeps the added newline support within the existing
// simple-command policy model. Reserved words, assignments, quoted/expanded names,
// functions, and grouping require a full shell grammar and must fail closed.
func simpleMultilineCommandHead(segment string) bool {
	head := segment
	if end := strings.IndexAny(head, " \t\r\n"); end >= 0 {
		head = head[:end]
	}
	if head == "" {
		return false
	}
	for _, char := range head {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("_./-", char) {
			continue
		}
		return false
	}
	switch head {
	case "if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done",
		"case", "in", "esac", "select", "function", "time", "coproc":
		return false
	}
	return true
}

// benignRedirectSkip reports how many bytes the scanner may step over when
// command[gt] is '>' opening a redirection that cannot change a segment's
// audited behavior: sinks to /dev/null (>, >>, N>, N>>) and fd duplication
// (>&1, >&2, N>&M). It returns 0 for file-writing redirections, which stay
// rejected. The command text is never rewritten; this only scopes scanning.
func benignRedirectSkip(command string, gt int) int {
	i := gt
	if i+1 < len(command) && command[i+1] == '>' {
		i++
	}
	i++
	for i < len(command) && (command[i] == ' ' || command[i] == '\t') {
		i++
	}
	rest := command[i:]
	switch {
	case strings.HasPrefix(rest, "/dev/null"):
		return i + len("/dev/null") - gt
	case len(rest) >= 2 && rest[0] == '&' && (rest[1] == '1' || rest[1] == '2' || rest[1] == '-'):
		return i + 2 - gt
	}
	return 0
}

// quotedHeredocCommandEnd recognizes the narrow heredoc form that can be
// treated as literal stdin without parsing a full shell grammar:
//
//	command <<'TAG'\n...literal body...\nTAG
//	command <<"TAG"\n...literal body...\nTAG
//
// Quoting the delimiter disables shell expansion in the heredoc body. The end
// includes the terminator's newline, so scanning resumes at the next command.
// Whitespace before the quoted delimiter and <<- tab stripping are supported.
func quotedHeredocCommandEnd(command string, operatorIndex int) (int, bool) {
	if operatorIndex+3 >= len(command) || command[operatorIndex] != '<' || command[operatorIndex+1] != '<' {
		return 0, false
	}
	quoteIndex := operatorIndex + 2
	stripTabs := command[quoteIndex] == '-'
	if stripTabs {
		quoteIndex++
	}
	for quoteIndex < len(command) && (command[quoteIndex] == ' ' || command[quoteIndex] == '\t') {
		quoteIndex++
	}
	if quoteIndex >= len(command) {
		return 0, false
	}
	quote := command[quoteIndex]
	if quote != '\'' && quote != '"' {
		return 0, false
	}
	delimiterStart := quoteIndex + 1
	delimiterEnd := delimiterStart
	for delimiterEnd < len(command) && command[delimiterEnd] != quote {
		if command[delimiterEnd] == '\n' || command[delimiterEnd] == '\r' {
			return 0, false
		}
		delimiterEnd++
	}
	if delimiterEnd == delimiterStart || delimiterEnd >= len(command) {
		return 0, false
	}
	delimiter := command[delimiterStart:delimiterEnd]
	cursor := delimiterEnd + 1
	for cursor < len(command) && (command[cursor] == ' ' || command[cursor] == '\t') {
		cursor++
	}
	if cursor >= len(command) {
		return 0, false
	}
	switch command[cursor] {
	case '\n':
		cursor++
	case '\r':
		if cursor+1 >= len(command) || command[cursor+1] != '\n' {
			return 0, false
		}
		cursor += 2
	default:
		return 0, false
	}

	for cursor <= len(command) {
		lineStart := cursor
		lineEnd := strings.IndexByte(command[lineStart:], '\n')
		next := len(command)
		if lineEnd >= 0 {
			lineEnd += lineStart
			next = lineEnd + 1
		} else {
			lineEnd = len(command)
		}
		line := strings.TrimSuffix(command[lineStart:lineEnd], "\r")
		if stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delimiter {
			return next, true
		}
		if lineEnd == len(command) {
			break
		}
		cursor = next
	}
	return 0, false
}

func autoAllowReadonlyEnabled(rules config.CommandRules) bool {
	return rules.AutoAllowReadonly == nil || *rules.AutoAllowReadonly
}

// isReadonlyCommand reports whether every supported compound segment is
// read-only. A single non-read-only segment falls back to normal policy.
func isReadonlyCommand(command string) bool {
	segments, unsafe := commandSegments(command)
	if unsafe || len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		if !isReadonlySegment(segment.Command) {
			return false
		}
	}
	return true
}

// isReadonlySegment accepts only commands with no side effects and no shell
// metacharacters (pipe, redirect, background, command substitution). Arguments
// that can be expanded by the shell are rejected because this matcher does not
// resolve them against the workspace before execution.
func isReadonlySegment(segment string) bool {
	trimmed := strings.TrimSpace(segment)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, "|&><`\n") || strings.Contains(trimmed, "$(") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "ls":
		return allReadonlyArguments(fields[1:])
	case "pwd":
		return len(fields) == 1
	case "cat", "head", "tail":
		return allReadonlyArguments(fields[1:])
	case "git":
		return isReadonlyGit(fields[1:])
	}
	return false
}

// isReadonlyGit accepts git -C <relative-path> <status|diff|log|show> and the
// bare <status|diff|log|show> forms. -C targets must be relative workspace
// paths.
func isReadonlyGit(args []string) bool {
	for len(args) > 0 {
		if args[0] != "-C" {
			break
		}
		if len(args) < 2 {
			return false
		}
		if !safeReadonlyArgument(args[1]) {
			return false
		}
		args = args[2:]
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "status", "diff", "log", "show":
		return readonlyGitArguments(args[1:])
	}
	return false
}

// Git 支持长选项缩写，且 diff/log/show 可被仓库配置的外部转换器放大。
// 未知或带副作用的选项（如 --output、--ext-diff、--textconv 及其缩写）
// 必须交回显式策略，不能仅凭安全字符就自动当作只读。
func readonlyGitArguments(args []string) bool {
	paths := false
	for _, arg := range args {
		if !safeReadonlyArgument(arg) {
			return false
		}
		if arg == "--" {
			paths = true
			continue
		}
		if paths || !strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "--short", "-s", "--branch", "-b", "--porcelain", "--porcelain=v1", "--porcelain=v2",
			"--oneline", "--stat", "--shortstat", "--numstat", "--name-only", "--name-status",
			"--summary", "--check", "--cached", "--staged", "--no-ext-diff", "--no-textconv",
			"--no-color", "--color=never", "--patch", "-p", "--no-patch", "--raw", "--abbrev-commit",
			"--graph", "--decorate", "--no-decorate", "--all", "--reverse", "--date-order", "--topo-order":
			continue
		default:
			return false
		}
	}
	return true
}

func allReadonlyArguments(args []string) bool {
	for _, arg := range args {
		if !safeReadonlyArgument(arg) {
			return false
		}
	}
	return true
}

func safeReadonlyArgument(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "/") || strings.Contains(arg, "..") {
		return false
	}
	// These characters trigger shell expansion or make the path differ from
	// the literal value reviewed by the policy matcher.
	return !strings.ContainsAny(arg, "$~{}\\*?[]'\"")
}

func matchAny(patterns []string, command string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		if re.MatchString(command) {
			return true
		}
	}
	return false
}

// MatchFile applies file path policy. Once any list is configured, unmatched
// paths require confirmation, making an allow list behave as a real whitelist.
func MatchFile(rules config.FileRules, path string) Decision {
	if matchAny(rules.Deny, path) {
		return Deny
	}
	if matchAny(rules.Confirm, path) {
		return Confirm
	}
	if matchAny(rules.Allow, path) {
		return Allow
	}
	if len(rules.Allow)+len(rules.Confirm)+len(rules.Deny) > 0 {
		return Confirm
	}
	return Allow
}
