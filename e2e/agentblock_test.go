package e2e

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
)

// The `lino help --agent` block is tested like code: every command example
// in it runs against a fixture root and must succeed, every synopsis runs
// with its placeholders filled in, and the outcome table must match the real
// outcome set and the exit codes the binary returns.

const agentBy = "agent-p04"

func TestAgentBlock(t *testing.T) {
	r := New(t).Run("help", "--agent")
	if r.Exit != 0 || !strings.HasPrefix(r.Stdout, "# lino agent instructions v") {
		t.Fatalf("help --agent: exit %d\n%s", r.Exit, r.Stdout)
	}
	p := parseAgentBlock(r.Stdout)
	t.Logf("%d examples, %d synopses, %d mentions, %d outcome rows", len(p.examples), len(p.synopses), len(p.mentions), len(p.rows))
	for _, p := range checkAgentBlock(t, r.Stdout, false) {
		t.Error(p)
	}
}

// TestAgentBlockBroken proves the check catches drift: each mutation of the
// real block must produce at least one problem.
func TestAgentBlockBroken(t *testing.T) {
	block := New(t).Run("help", "--agent").Stdout
	cases := []struct{ name, old, new string }{
		{"flag typo", "--after 44:e42", "--afterr 44:e42"},
		{"alternative flag typo", "--at-start", "--at-top"},
		{"separator typo", "<<<lino>>>", "<<<lino>>"},
		{"unknown command", "lino show <id>", "lino shw <id>"},
		{"synopsis flag typo", "[--depth N]", "[--deep N]"},
		{"header format", "lines 40-60 of 240", "lines 40..60 of 240"},
		{"wrong exit code", "conflict (6)", "conflict (7)"},
		{"missing outcome", "  refused (7)", "  (7)"},
		{"unknown outcome", "live_exists (9)", "live_exist (9)"},
		{"bad mention", "lino ls, lino history", "lino ls, lino hist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(block, c.old) {
				t.Fatalf("block no longer contains %q; update this case", c.old)
			}
			ps := checkAgentBlock(t, strings.Replace(block, c.old, c.new, 1), true)
			if len(ps) == 0 {
				t.Fatalf("replacing %q with %q went unnoticed", c.old, c.new)
			}
			t.Log(strings.SplitN(ps[0], "\n", 2)[0])
		})
	}
}

// blockExample is one runnable command line from the block.
type blockExample struct {
	line  int
	args  []string // without the leading "lino"
	stdin string
}

type blockRow struct {
	names []outcome.Outcome
	code  int
	text  string
}

type parsedBlock struct {
	examples []blockExample
	synopses []string // text after "lino ", placeholders unresolved
	mentions []string // bare command names in prose
	rows     []blockRow
	problems []string
}

var (
	descSep    = regexp.MustCompile(`\s{2,}`)
	mentionRe  = regexp.MustCompile(`(?:: |, |\(|printed )lino ([a-z]+)\b`)
	rowRe      = regexp.MustCompile(`^  ([a-z_]+(?:, [a-z_]+)*) \((\d+)\)\s+(.*)$`)
	anchorTok  = regexp.MustCompile(`^(\d+):[0-9A-Za-z]+$`)
	versionTok = regexp.MustCompile(`v=[0-9A-Za-z]+`)
	newVersion = regexp.MustCompile(`v=[0-9A-Za-z]+→[0-9A-Za-z]+`)
	anchorLine = regexp.MustCompile(`^\d+:[0-9A-Za-z]+\| `)
)

func parseAgentBlock(block string) parsedBlock {
	var p parsedBlock
	lines := strings.Split(block, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		prose := line
		if strings.HasPrefix(trim, "lino ") {
			prose = strings.TrimPrefix(trim, descSep.Split(trim, 2)[0])
		}
		for _, m := range mentionRe.FindAllStringSubmatch(prose, -1) {
			p.mentions = append(p.mentions, m[1])
		}
		switch {
		case strings.HasPrefix(trim, "lino "):
			text := descSep.Split(trim, 2)[0]
			if strings.Contains(text, "<<'EOF'") {
				start, indent := i+1, len(line)-len(strings.TrimLeft(line, " "))
				var body []string
				for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "EOF"; i++ {
					if len(lines[i]) < indent || strings.TrimSpace(lines[i][:indent]) != "" {
						p.problems = append(p.problems, fmt.Sprintf("line %d: heredoc body not indented like its command", i+1))
					}
					body = append(body, strings.TrimPrefix(lines[i], strings.Repeat(" ", indent)))
				}
				if i == len(lines) {
					p.problems = append(p.problems, fmt.Sprintf("heredoc after %q never ends", text))
				}
				args := strings.Fields(strings.Replace(text, "<<'EOF'", "", 1))[1:]
				p.examples = append(p.examples, blockExample{line: start, args: args, stdin: strings.Join(body, "\n") + "\n"})
				continue
			}
			if strings.ContainsAny(text, "<[|") {
				p.synopses = append(p.synopses, strings.TrimPrefix(text, "lino "))
				continue
			}
			if strings.ContainsAny(text, `'"`) {
				p.problems = append(p.problems, fmt.Sprintf("line %d: quoted arguments are not supported by this test", i+1))
			}
			p.examples = append(p.examples, blockExample{line: i + 1, args: strings.Fields(text)[1:]})
			continue
		case strings.Contains(line, "->"):
			syn := strings.TrimSpace(strings.SplitN(line, "->", 2)[1])
			if !strings.HasPrefix(syn, "lino ") {
				p.problems = append(p.problems, fmt.Sprintf("line %d: mapping target is not a lino command", i+1))
			}
			p.synopses = append(p.synopses, strings.TrimPrefix(syn, "lino "))
			continue
		case strings.HasPrefix(trim, "(or ") && strings.HasSuffix(trim, ")"):
			if len(p.examples) == 0 {
				p.problems = append(p.problems, fmt.Sprintf("line %d: alternatives without an example", i+1))
				continue
			}
			base := p.examples[len(p.examples)-1]
			for _, alt := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(trim, "(or "), ")"), ", ") {
				args, ok := withAlternative(base.args, strings.Fields(alt))
				if !ok {
					p.problems = append(p.problems, fmt.Sprintf("line %d: no flag to replace with %q", i+1, alt))
					continue
				}
				p.examples = append(p.examples, blockExample{line: i + 1, args: args, stdin: base.stdin})
			}
			continue
		}
		if m := rowRe.FindStringSubmatch(line); m != nil {
			code, _ := strconv.Atoi(m[2])
			row := blockRow{code: code, text: m[3]}
			for _, n := range strings.Split(m[1], ", ") {
				row.names = append(row.names, outcome.Outcome(n))
			}
			p.rows = append(p.rows, row)
		}
	}
	return p
}

// withAlternative replaces the first flag of args (and its value, if any)
// with alt. A single capital letter in alt stands for the replaced value.
func withAlternative(args, alt []string) ([]string, bool) {
	for j := 1; j < len(args); j++ {
		if !strings.HasPrefix(args[j], "--") {
			continue
		}
		span, val := 1, ""
		if j+1 < len(args) && !strings.HasPrefix(args[j+1], "--") {
			span, val = 2, args[j+1]
		}
		out := append([]string{}, args[:j]...)
		for _, a := range alt {
			if len(a) == 1 && a >= "A" && a <= "Z" {
				a = val
			}
			out = append(out, a)
		}
		return append(out, args[j+span:]...), true
	}
	return nil, false
}

// checkAgentBlock runs everything the block claims and returns the problems.
// With firstOnly it stops after the first phase that finds any.
func checkAgentBlock(t *testing.T, block string, firstOnly bool) []string {
	p := parseAgentBlock(block)
	probs := p.problems
	if len(p.examples) < 8 || len(p.synopses) < 5 || len(p.rows) < 5 {
		probs = append(probs, fmt.Sprintf("parsed only %d examples, %d synopses, %d outcome rows",
			len(p.examples), len(p.synopses), len(p.rows)))
	}
	c := &blockChecker{t: t, tmpl: agentTemplate(t)}
	for _, phase := range []func() []string{
		func() []string { return checkOutcomeTable(p.rows) },
		func() []string { return c.checkMentions(p.mentions) },
		func() []string { return c.checkSynopses(p.synopses) },
		func() []string { return c.checkExamples(block, p.examples) },
		func() []string { return c.checkOutcomes(p.rows) },
	} {
		if firstOnly && len(probs) > 0 {
			break
		}
		probs = append(probs, phase()...)
	}
	return probs
}

type blockChecker struct {
	t    *testing.T
	tmpl string // seeded root, copied for every command
}

// agentTemplate builds an indexed fixture root with two recorded changes by agentBy.
func agentTemplate(t *testing.T) string {
	t.Helper()
	var app strings.Builder
	for i := 1; i <= 240; i++ {
		if i == 100 {
			app.WriteString("exact old lines, matched once\n")
			continue
		}
		fmt.Fprintf(&app, "\tline %d\n", i)
	}
	h := NewInit(t).Write("src/app.go", app.String()).Write("src/old.go", "package src\n\nvar old = 1\n")
	h.Env["LINO_BY"] = agentBy
	h.must(0, Cmd{Args: []string{"index", "--direct"}})
	for _, f := range []string{"src/app.go", "src/old.go"} {
		h.must(0, Cmd{Args: []string{"edit", f, "1", "1", "--v", h.fileV(f), "--direct"}, Stdin: "// seeded\n"})
	}
	return h.Root
}

// root is a fresh copy of the template.
func (c *blockChecker) root() *Harness {
	h := New(c.t)
	h.Env["LINO_BY"] = agentBy
	err := filepath.WalkDir(c.tmpl, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(c.tmpl, p)
		dst := filepath.Join(h.Root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return h
}

func direct(args []string) []string {
	if len(args) > 0 && (args[0] == "help" || args[0] == "init") {
		return args
	}
	return append(args, "--direct")
}

// resolve fills in real versions and anchors for the placeholders in args.
func resolve(h *Harness, args []string) []string {
	out := append([]string{}, args...)
	file := ""
	if len(out) > 1 && !strings.HasPrefix(out[1], "-") {
		file = out[1]
	}
	for i := 1; i < len(out); i++ {
		switch {
		case out[i-1] == "--v" && file != "":
			if !exists(h, file) { // e.g. rm of a file an earlier example created
				h.Write(file, "package src\n")
			}
			out[i] = h.fileV(file)
		case out[i-1] != "--lines" && anchorTok.MatchString(out[i]) && file != "":
			n := anchorTok.FindStringSubmatch(out[i])[1]
			r := h.must(0, Cmd{Args: []string{"read", file, "--lines", n + ":" + n, "--anchors", "--json", "--direct"}})
			var env struct {
				Data struct{ Anchors []string } `json:"data"`
			}
			if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil || len(env.Data.Anchors) != 1 {
				h.t.Fatalf("anchor of %s:%s: %v\n%s", file, n, err, r.Stdout)
			}
			out[i] = n + ":" + env.Data.Anchors[0]
		}
	}
	return out
}

func exists(h *Harness, rel string) bool {
	for _, f := range h.Tree() {
		if f == rel {
			return true
		}
	}
	return false
}

func (c *blockChecker) checkExamples(block string, exs []blockExample) []string {
	var probs []string
	header, lineFmt := claim(block, "The header gives the version: "), claim(block, "Each line is ")
	if header == "" || lineFmt != "<n>:<anchor>| text" {
		probs = append(probs, "the block no longer describes the read header and line format as this test expects")
	}
	headerRe := regexp.MustCompile("^" + versionTok.ReplaceAllString(regexp.QuoteMeta(header), `v=[0-9A-Za-z]+`) + "$")
	for _, ex := range exs {
		for _, js := range []bool{false, true} {
			h := c.root()
			args := resolve(h, ex.args)
			if js {
				args = append(args, "--json")
			}
			r := h.Exec(Cmd{Args: direct(args), Stdin: ex.stdin})
			where := fmt.Sprintf("block line %d: lino %s", ex.line, strings.Join(ex.args, " "))
			if r.Exit != 0 {
				probs = append(probs, fmt.Sprintf("%s: exit %d\n%s", where, r.Exit, h.Transcript(r)))
				continue
			}
			if js {
				var env struct {
					OK      bool            `json:"ok"`
					Outcome outcome.Outcome `json:"outcome"`
				}
				if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil || !env.OK || !env.Outcome.Valid() {
					probs = append(probs, fmt.Sprintf("%s --json: bad envelope (%v)\n%s", where, err, r.Stdout))
				}
				continue
			}
			out := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
			switch ex.args[0] {
			case "read":
				if !contains1(ex.args, "--anchors") {
					break
				}
				if !headerRe.MatchString(out[0]) {
					probs = append(probs, fmt.Sprintf("%s: header %q does not look like %q", where, out[0], header))
				}
				for _, l := range out[1:] {
					if !anchorLine.MatchString(l) {
						probs = append(probs, fmt.Sprintf("%s: line %q is not %q", where, l, lineFmt))
						break
					}
				}
			case "edit", "insert", "delete", "replace":
				if !newVersion.MatchString(r.Stdout) {
					probs = append(probs, fmt.Sprintf("%s: no v=old→new in %q", where, r.Stdout))
				}
			}
		}
	}
	return probs
}

// claim returns the quoted text following prefix in block.
func claim(block, prefix string) string {
	i := strings.Index(block, prefix+`"`)
	if i < 0 {
		return ""
	}
	rest := block[i+len(prefix)+1:]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

func contains1(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

type synItem struct {
	optional bool
	alts     [][]string
}

func tokenizeSynopsis(s string) []synItem {
	var items []synItem
	for s = strings.TrimSpace(s); s != ""; s = strings.TrimSpace(s) {
		if s[0] == '[' {
			end := strings.IndexByte(s, ']')
			if end < 0 {
				end = len(s) - 1
			}
			it := synItem{optional: true}
			for _, a := range strings.Split(s[1:end], "|") {
				it.alts = append(it.alts, strings.Fields(a))
			}
			items = append(items, it)
			s = s[end+1:]
			continue
		}
		tok := s
		if k := strings.IndexAny(s, " ["); k >= 0 {
			tok = s[:k]
		}
		if strings.HasPrefix(tok, "<") && !strings.Contains(tok, ">") { // <your name>
			tok = s[:strings.IndexByte(s, '>')+1]
		}
		items = append(items, synItem{alts: [][]string{{tok}}})
		s = s[len(tok):]
	}
	return items
}

// expandSynopsis returns the required-only form, the form with every option
// and one form per extra alternative.
func expandSynopsis(items []synItem) [][]string {
	build := func(opts bool, pick map[int]int) []string {
		var out []string
		for i, it := range items {
			if it.optional && !opts {
				continue
			}
			out = append(out, it.alts[pick[i]]...)
		}
		return out
	}
	forms := [][]string{build(false, nil), build(true, nil)}
	for i, it := range items {
		for k := 1; k < len(it.alts); k++ {
			forms = append(forms, build(true, map[int]int{i: k}))
		}
	}
	return forms
}

func fillPlaceholders(h *Harness, args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i, a := range args {
		switch {
		case strings.HasPrefix(a, "-"):
		case a == "<file>" || (i > 0 && a == "path"):
			a = "src/app.go"
		case a == "<text>":
			a = "line"
		case a == "<id>":
			a = strconv.FormatInt(latestID(h), 10)
		case i > 0 && a == "dir":
			a = "src"
		case a == "N":
			a = "1"
		case a == "GLOB":
			a = "src/*"
		case a == "A:B":
			a = "40:60"
		case strings.HasPrefix(a, "<") || (strings.ToUpper(a) == a && strings.ToLower(a) != a) ||
			(i > 0 && !strings.HasPrefix(a, "-") && strings.ToLower(a) == a && !isValue(args[i-1])):
			return nil, fmt.Errorf("unknown placeholder %q", a)
		}
		out = append(out, a)
	}
	return out, nil
}

// isValue reports whether the token after flag is a flag value, not a positional.
func isValue(flag string) bool { return strings.HasPrefix(flag, "-") }

func latestID(h *Harness) int64 {
	r := h.must(0, Cmd{Args: []string{"history", "--json", "-k", "1", "--direct"}})
	var env struct {
		Data struct {
			Changes []struct{ ID int64 } `json:"changes"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil || len(env.Data.Changes) == 0 {
		h.t.Fatalf("history: %v\n%s", err, r.Stdout)
	}
	return env.Data.Changes[0].ID
}

func (c *blockChecker) checkSynopses(syns []string) []string {
	var probs []string
	for _, s := range syns {
		items := tokenizeSynopsis(s)
		if len(items) > 1 && items[1].alts[0][0] == "|" { // a list of commands
			for _, it := range items {
				if name := it.alts[0][0]; name != "|" {
					probs = append(probs, c.checkHelp(name)...)
				}
			}
			continue
		}
		for _, form := range expandSynopsis(items) {
			h := c.root()
			args, err := fillPlaceholders(h, form)
			if err != nil {
				probs = append(probs, fmt.Sprintf("lino %s: %v", s, err))
				break
			}
			if r := h.Exec(Cmd{Args: direct(args)}); r.Exit != 0 {
				probs = append(probs, fmt.Sprintf("synopsis lino %s: exit %d\n%s", s, r.Exit, h.Transcript(r)))
			}
		}
	}
	return probs
}

func (c *blockChecker) checkHelp(name string) []string {
	h := New(c.t)
	if r := h.Run("help", name); r.Exit != 0 {
		return []string{fmt.Sprintf("block names unknown command %q\n%s", name, h.Transcript(r))}
	}
	return nil
}

func (c *blockChecker) checkMentions(names []string) []string {
	var probs []string
	for _, name := range names {
		h := c.root()
		if name == "init" {
			h = New(c.t)
		}
		if r := h.Exec(Cmd{Args: direct([]string{name})}); r.Exit != 0 {
			probs = append(probs, fmt.Sprintf("mentioned lino %s: exit %d\n%s", name, r.Exit, h.Transcript(r)))
		}
	}
	return probs
}

// outcomeTriggers provoke each outcome for real. live_exists and internal are
// not provoked here: the first needs a live process, the second a bug.
var outcomeTriggers = map[outcome.Outcome]func(h *Harness) Cmd{
	outcome.OK: func(h *Harness) Cmd { return Cmd{Args: []string{"read", "src/old.go"}} },
	outcome.Updated: func(h *Harness) Cmd {
		return Cmd{Args: []string{"edit", "src/old.go", "1", "1", "--v", h.fileV("src/old.go")}, Stdin: "x\n"}
	},
	outcome.Created: func(h *Harness) Cmd { return Cmd{Args: []string{"write", "src/fresh.go"}, Stdin: "x\n"} },
	outcome.Empty:   func(h *Harness) Cmd { return Cmd{Args: []string{"search", "zzqqxx_nowhere"}} },
	outcome.Truncated: func(h *Harness) Cmd {
		h.Write("big.txt", strings.Repeat("row\n", 1200))
		return Cmd{Args: []string{"read", "big.txt"}}
	},
	outcome.Usage:    func(h *Harness) Cmd { return Cmd{Args: []string{"read"}} },
	outcome.NotFound: func(h *Harness) Cmd { return Cmd{Args: []string{"read", "src/missing.go"}} },
	outcome.AnchorMismatch: func(h *Harness) Cmd {
		return Cmd{Args: []string{"delete", "src/app.go", "42:zzz", "42:zzz", "--v", h.fileV("src/app.go")}}
	},
	outcome.Ambiguous: func(h *Harness) Cmd {
		h.Write("dup.txt", "dup\nx\ndup\n")
		return Cmd{Args: []string{"replace", "dup.txt", "--v", h.fileV("dup.txt")}, Stdin: "dup\n<<<lino>>>\ny\n"}
	},
	outcome.Conflict: func(h *Harness) Cmd {
		return Cmd{Args: []string{"edit", "src/old.go", "1", "1", "--v", "000000"}, Stdin: "x\n"}
	},
	outcome.Refused:    func(h *Harness) Cmd { return Cmd{Args: []string{"read", "../outside.txt"}} },
	outcome.NotRunning: func(h *Harness) Cmd { return Cmd{Args: []string{"read", "x.txt"}} }, // in an uninitialised root
}

func checkOutcomeTable(rows []blockRow) []string {
	var probs []string
	seen := map[outcome.Outcome]int{}
	for _, row := range rows {
		for _, o := range row.names {
			if !o.Valid() {
				probs = append(probs, fmt.Sprintf("outcome table lists unknown outcome %q", o))
				continue
			}
			if _, dup := seen[o]; dup {
				probs = append(probs, fmt.Sprintf("outcome %q listed twice", o))
			}
			seen[o] = row.code
			if row.code != o.ExitCode() {
				probs = append(probs, fmt.Sprintf("outcome table: %s (%d), real exit code %d", o, row.code, o.ExitCode()))
			}
		}
	}
	for _, o := range outcome.All {
		if _, ok := seen[o]; !ok {
			probs = append(probs, fmt.Sprintf("outcome %q (exit %d) missing from the table", o, o.ExitCode()))
		}
	}
	return probs
}

// checkOutcomes provokes each listed outcome and compares with the table.
func (c *blockChecker) checkOutcomes(rows []blockRow) []string {
	var probs []string
	for _, row := range rows {
		for _, o := range row.names {
			trigger, ok := outcomeTriggers[o]
			if !ok {
				continue
			}
			h := c.root()
			if o == outcome.NotRunning {
				h = New(c.t)
			}
			cmd := trigger(h)
			cmd.Args = append(direct(cmd.Args), "--json")
			r := h.Exec(cmd)
			var env struct {
				Outcome outcome.Outcome `json:"outcome"`
			}
			_ = json.Unmarshal([]byte(r.Stdout), &env)
			if env.Outcome != o || r.Exit != row.code {
				probs = append(probs, fmt.Sprintf("table says %s exits %d; provoking it gave %q, exit %d\n%s",
					o, row.code, env.Outcome, r.Exit, h.Transcript(r)))
			}
			if o == outcome.Truncated {
				if hint := regexp.MustCompile(`--lines [0-9]+:[0-9]+`).FindString(row.text); hint != "" {
					r := h.Exec(Cmd{Args: direct([]string{"read", "big.txt"})})
					if !strings.Contains(r.Stdout+r.Stderr, hint) {
						probs = append(probs, fmt.Sprintf("truncated read does not print the hint %q\n%s", hint, h.Transcript(r)))
					}
				}
			}
		}
	}
	return probs
}

func TestAgentBlockParsing(t *testing.T) {
	alts := []struct {
		args, alt, want string
	}{
		{"insert f --after 4:ab --v V", "--before A", "insert f --before 4:ab --v V"},
		{"insert f --after 4:ab --v V", "--at-end", "insert f --at-end --v V"},
		{"rm f --v V", "--force", "rm f --force"},
	}
	for _, c := range alts {
		got, ok := withAlternative(strings.Fields(c.args), strings.Fields(c.alt))
		if !ok || strings.Join(got, " ") != c.want {
			t.Errorf("withAlternative(%q, %q) = %q", c.args, c.alt, got)
		}
	}
	syn := []struct {
		in   string
		want []string
	}{
		{"read <file> [--lines A:B]", []string{"read <file>", "read <file> --lines A:B"}},
		{"search <text> [--regex | --words] [-k N]", []string{"search <text>", "search <text> --regex -k N", "search <text> --words -k N"}},
		{"history [path]", []string{"history", "history path"}},
	}
	for _, c := range syn {
		var got []string
		for _, f := range expandSynopsis(tokenizeSynopsis(c.in)) {
			got = append(got, strings.Join(f, " "))
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("expand %q = %q, want %q", c.in, got, c.want)
		}
	}
}
