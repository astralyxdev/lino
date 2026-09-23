package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/astralyx/lino/internal/outcome"
)

// StringList is a repeatable string flag (e.g. --path P --path Q).
type StringList []string

func (s *StringList) String() string     { return fmt.Sprint([]string(*s)) }
func (s *StringList) Set(v string) error { *s = append(*s, v); return nil }
func (s *StringList) Values() []string   { return *s }

// MaxStdin caps what ReadStdin accepts.
const MaxStdin = 64 << 20

// ReadStdin reads all of the call's stdin. A terminal on stdin (no heredoc
// or pipe) is a usage error instead of a hang; over MaxStdin is refused.
func (c *Call) ReadStdin() ([]byte, error) {
	return ReadStdin(c.Stdin, MaxStdin)
}

// ReadStdin reads r up to max bytes.
func ReadStdin(r io.Reader, max int64) ([]byte, error) {
	if f, ok := r.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			return nil, outcome.New(outcome.Usage, "expected content on stdin").WithHint("pass it with a heredoc: <<'EOF' ... EOF")
		}
	}
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, outcome.Wrap(outcome.Internal, err, "read stdin: "+err.Error())
	}
	if int64(len(b)) > max {
		return nil, outcome.New(outcome.Refused, "stdin larger than %d bytes", max)
	}
	return b, nil
}
