package tokens

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// Arm is one tool set an agent works with.
type Arm struct {
	Name   string   `json:"name"`
	Prompt string   `json:"prompt"` // appended to the agent's system prompt
	Deny   []string `json:"deny"`   // commands shadowed on PATH by a stub that fails
	Lino   bool     `json:"lino"`   // lino on PATH, root initialised, help --agent block in the prompt
}

const armBase = "You are working in a checkout of the Go standard library source (the src/ " +
	"directory of golang/go at go1.24.0); your working directory is its root. Do only what " +
	"the task asks, change no other files, and do not build or test (the local Go toolchain " +
	"cannot build this tree). Work only through the bash tool. "

// Arms are the three arms of the token benchmark.
var Arms = []Arm{
	{Name: "grep", Deny: []string{"rg", "lino"},
		Prompt: armBase + "Use standard shell tools for files: grep, find, ls, cat, head, tail, sed, awk."},
	{Name: "rg", Deny: []string{"lino"},
		Prompt: armBase + "Use ripgrep (rg) to search, plus standard shell tools: find, ls, cat, head, tail, sed, awk."},
	{Name: "lino", Deny: []string{"rg"}, Lino: true,
		Prompt: armBase + "Use lino for every file operation; its instructions follow."},
}

// GetArm returns the arm called name.
func GetArm(name string) (Arm, bool) {
	for _, a := range Arms {
		if a.Name == name {
			return a, true
		}
	}
	return Arm{}, false
}

// Usage is token usage summed over a run.
type Usage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

// Total is every token the run was billed for, cached input included.
func (u Usage) Total() int64 { return u.Input + u.Output + u.CacheCreation + u.CacheRead }

// Ops counts the file operations in a transcript by kind.
type Ops struct {
	Lino     int `json:"lino"`
	Fallback int `json:"fallback"` // cat, grep, sed, find... (the tools lino replaces)
	Other    int `json:"other"`    // commands that are not file operations
	Commands int `json:"commands"` // bash tool calls
	Retries  int `json:"retries"`  // lino calls answered conflict or anchor_mismatch
}

// FallbackRate is the share of file operations done without lino.
func (o Ops) FallbackRate() float64 {
	if n := o.Lino + o.Fallback; n > 0 {
		return float64(o.Fallback) / float64(n)
	}
	return 0
}

// Transcript is what a run of the agent reported.
type Transcript struct {
	Usage    Usage    `json:"usage"`
	Turns    int      `json:"turns"`
	CostUSD  float64  `json:"cost_usd"`
	IsError  bool     `json:"is_error"`
	Result   string   `json:"result,omitempty"`
	Ops      Ops      `json:"ops"`
	Commands []string `json:"commands"`
}

// ParseStream reads Claude Code's --output-format stream-json: one JSON event
// per line. Bash tool calls are collected from assistant events, their
// results matched by tool_use_id, and usage taken from the final result
// event.
func ParseStream(r io.Reader) (Transcript, error) {
	var tr Transcript
	pending := map[string]string{} // tool_use_id -> command
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type      string          `json:"type"`
					ID        string          `json:"id"`
					Name      string          `json:"name"`
					Input     json.RawMessage `json:"input"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
				} `json:"content"`
			} `json:"message"`
			Usage    Usage   `json:"usage"`
			NumTurns int     `json:"num_turns"`
			Cost     float64 `json:"total_cost_usd"`
			IsError  bool    `json:"is_error"`
			Result   string  `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue // not an event
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type != "tool_use" || c.Name != "Bash" {
					continue
				}
				var in struct{ Command string }
				_ = json.Unmarshal(c.Input, &in)
				pending[c.ID] = in.Command
				tr.Commands = append(tr.Commands, in.Command)
				tr.Ops.add(Classify(in.Command))
				tr.Ops.Commands++
			}
		case "user":
			for _, c := range ev.Message.Content {
				cmd, ok := pending[c.ToolUseID]
				if c.Type != "tool_result" || !ok {
					continue
				}
				if Classify(cmd).Lino > 0 && retryOutcome.MatchString(resultText(c.Content)) {
					tr.Ops.Retries++
				}
			}
		case "result":
			tr.Usage, tr.Turns, tr.CostUSD, tr.IsError, tr.Result = ev.Usage, ev.NumTurns, ev.Cost, ev.IsError, ev.Result
		}
	}
	return tr, sc.Err()
}

var retryOutcome = regexp.MustCompile(`(?m)^(conflict|anchor_mismatch):|"outcome":\s*"(conflict|anchor_mismatch)"`)

// resultText flattens a tool_result content: a string or a list of text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct{ Text string }
	_ = json.Unmarshal(raw, &blocks)
	var b strings.Builder
	for _, bl := range blocks {
		b.WriteString(bl.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func (o *Ops) add(n Ops) {
	o.Lino += n.Lino
	o.Fallback += n.Fallback
	o.Other += n.Other
}

// fileTools are the commands lino replaces (scope.md: cat, grep, find, sed).
var fileTools = map[string]bool{
	"cat": true, "head": true, "tail": true, "less": true, "more": true, "nl": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"find": true, "ls": true, "tree": true, "fd": true,
	"sed": true, "awk": true, "perl": true, "ed": true, "ex": true, "tee": true,
	"mv": true, "rm": true, "cp": true, "touch": true, "wc": true, "python": true, "python3": true,
}

var splitCmd = regexp.MustCompile(`\|\||&&|[|;\n]|\$\(|` + "`")

// Classify counts the simple commands in a shell command line: lino calls,
// file operations lino replaces, and the rest. Heredoc bodies are skipped;
// `cat > f <<EOF` counts as a fallback write.
func Classify(cmdline string) Ops {
	var o Ops
	for _, part := range splitCmd.Split(stripHeredocs(cmdline), -1) {
		name := commandName(part)
		switch {
		case name == "":
		case name == "lino":
			o.Lino++
		case fileTools[name]:
			o.Fallback++
		default:
			o.Other++
		}
	}
	return o
}

var heredoc = regexp.MustCompile(`(?:^|[^<])<<-?[ \t]*['"]?(\w+)`) // not <<< here-strings

// stripHeredocs drops the bodies of heredocs, keeping the command lines.
func stripHeredocs(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		if m := heredoc.FindStringSubmatch(lines[i]); m != nil {
			for i++; i < len(lines) && strings.TrimSpace(lines[i]) != m[1]; i++ {
			}
		}
	}
	return strings.Join(out, "\n")
}

// commandName is the program a simple command runs, past env assignments,
// sudo/xargs/time wrappers, subshell parentheses and paths.
func commandName(part string) string {
	fields := strings.Fields(strings.Trim(strings.TrimSpace(part), "(){} "))
	for len(fields) > 0 {
		f := fields[0]
		switch {
		case strings.Contains(f, "=") && !strings.HasPrefix(f, "-"),
			f == "sudo", f == "time", f == "xargs", f == "command", f == "exec", f == "env",
			f == "then", f == "do", f == "else", f == "!", f == "if", f == "while", f == "until":
			fields = fields[1:]
			continue
		case f == "for", f == "done", f == "fi", f == "esac", f == "case", f == "select":
			return "" // shell syntax, not a command
		}
		if i := strings.LastIndexByte(f, '/'); i >= 0 {
			f = f[i+1:]
		}
		return f
	}
	return ""
}
