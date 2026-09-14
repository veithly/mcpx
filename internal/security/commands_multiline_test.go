package security

import (
	"strings"
	"testing"

	"mcpx/internal/config"
)

func TestAnalyzeCommandAuditsSimpleMultilineCommands(t *testing.T) {
	rules := config.CommandRules{Default: "allow", Confirm: []string{`^docker\b`}, Deny: []string{`^rm\b`}}
	cases := []struct {
		name      string
		command   string
		operators []string
		want      Decision
	}{
		{"lines", "git status\ngit diff", []string{"\n", ""}, Allow},
		{"blank lines", "\n\t\ngit status\n\n git diff\n\n", []string{"\n", ""}, Allow},
		{"and continuation", "git status &&\n git diff", []string{"&&", ""}, Allow},
		{"or continuation", "git status ||\n git diff", []string{"||", ""}, Allow},
		{"pipeline continuation", "git status |\n cat", []string{"|", ""}, Allow},
		{"semicolon newline", "git status;\n git diff", []string{";", ""}, Allow},
		{"quoted newline", "printf 'literal\ntext'\ngit status", []string{"\n", ""}, Allow},
		{"confirm later line", "git status\ndocker ps", []string{"\n", ""}, Confirm},
		{"deny later line", "git status\nrm -rf x", []string{"\n", ""}, Deny},
		{"audit after deny", "git status\nrm -rf x\ndocker ps", []string{"\n", "\n", ""}, Deny},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			analysis := AnalyzeCommand(rules, test.command)
			if analysis.Unsafe || analysis.Decision != test.want || len(analysis.Segments) != len(test.operators) {
				t.Fatalf("analysis=%+v, want decision=%s and %d segments", analysis, test.want, len(test.operators))
			}
			for index, segment := range analysis.Segments {
				if segment.Operator != test.operators[index] || segment.Decision != matchSegment(rules, segment.Command) {
					t.Errorf("segment %d=%+v", index, segment)
				}
			}
			if HasUnsafeShellOperator(test.command) {
				t.Fatal("supported command list reported unsafe")
			}
		})
	}
	if got := MatchCommand(config.CommandRules{}, "git status\necho needs-confirmation"); got != Confirm {
		t.Fatalf("unmatched later line must retain confirmation: %s", got)
	}
	if got := MatchCommand(config.CommandRules{Default: "deny"}, "git status\ngit diff"); got != Allow {
		t.Fatalf("readonly lines must retain readonly policy: %s", got)
	}
}

func TestAnalyzeCommandFlagsUnsupportedMultilineSyntax(t *testing.T) {
	rules := config.CommandRules{Default: "allow"}
	for _, command := range []string{
		"\n\n", "git status &&\n", "git status |\n", "git status\n; git diff",
		"git status\r\ngit diff",
		"git status\nif true; then echo hidden; fi",
		"git status\nfor x in a; do echo hidden; done",
		"git status\n{ echo hidden; }",
		"git status\n(echo hidden)",
		"git status\nf() { echo hidden; }",
		"git status\n! echo hidden",
		"git status\nNAME=value echo hidden",
		"git status\n\"echo\" hidden",
		"git status\ne\\cho hidden",
		"git status\n${COMMAND} hidden",
		"git status # '\necho hidden\n'",
		"# comment\ngit status",
		"git status\necho $(id)",
		"git status\necho `id`",
		"git status\ncat > output.txt",
		"git status\nsleep 1 &",
	} {
		if analysis := AnalyzeCommand(rules, command); analysis.Decision != Allow || !analysis.Unsafe {
			t.Errorf("unsupported command %q: got %+v, want unsafe allow under allow default", command, analysis)
		}
	}
	denyRules := config.CommandRules{Default: "allow", Deny: []string{`^git status`}}
	for _, command := range []string{"git status\r\ngit diff", "git status\necho $(id)"} {
		if analysis := AnalyzeCommand(denyRules, command); analysis.Decision != Deny || !analysis.Unsafe {
			t.Errorf("unsupported command %q: got %+v, want unsafe deny via raw-text deny match", command, analysis)
		}
	}
}

func TestAnalyzeCommandAuditsCommandsAfterQuotedHeredocs(t *testing.T) {
	rules := config.CommandRules{Default: "allow", Confirm: []string{`^docker\b`}, Deny: []string{`^rm\b`}}
	for _, header := range []string{"cat <<'TEXT'", "cat << \"TEXT\"", "cat <<-'TEXT'", "cat <<- 'TEXT'"} {
		for _, suffix := range []struct {
			command string
			want    Decision
		}{
			{"git status", Allow}, {"docker ps", Confirm}, {"rm -rf x", Deny},
		} {
			command := header + "\n$(literal) `literal` | ; &&\nTEXT\n" + suffix.command
			analysis := AnalyzeCommand(rules, command)
			if analysis.Unsafe || analysis.Decision != suffix.want || len(analysis.Segments) != 2 {
				t.Fatalf("%q: %+v", command, analysis)
			}
			if analysis.Segments[0].Operator != "\n" || analysis.Segments[1].Operator != "" || analysis.Segments[1].Command != suffix.command {
				t.Fatalf("heredoc tail was not independently audited: %+v", analysis.Segments)
			}
			if !strings.Contains(analysis.Segments[0].Command, "$(literal)") {
				t.Fatal("literal stdin must remain in the audited command")
			}
		}
	}
	for _, command := range []string{
		"cat <<-'TEXT'\n\tbody\n\tTEXT\ngit status",
		"cat << 'ONE'\nfirst\nONE\ncat << 'TWO'\nsecond\nTWO\n",
	} {
		analysis := AnalyzeCommand(rules, command)
		if analysis.Unsafe || analysis.Decision != Allow || len(analysis.Segments) != 2 {
			t.Fatalf("quoted heredoc sequence %q: %+v", command, analysis)
		}
	}
}
