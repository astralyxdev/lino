package mutate

import (
	"github.com/astralyx/lino/internal/anchor"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/textfile"
)

// Splice replaces the anchored range Start..End with Lines. With no Lines it
// deletes the range. OpName defaults to "edit", or "delete" when Lines is nil.
type Splice struct {
	Start, End anchor.Anchor
	Lines      []string
	OpName     string
}

func (s *Splice) Name() string {
	switch {
	case s.OpName != "":
		return s.OpName
	case s.Lines == nil:
		return "delete"
	}
	return "edit"
}

func (s *Splice) Target(lines []string) (Range, error) {
	r, err := anchor.ResolveRange(lines, s.Start, s.End)
	if err != nil {
		return Range{}, err
	}
	return Range{Start: r.Start, End: r.End}, nil
}

func (s *Splice) Addressed() (Range, bool) {
	return Range{Start: s.Start.Line, End: s.End.Line}, true
}

func (s *Splice) Apply(doc *textfile.Doc, t Range) (Range, error) {
	doc.Replace(t.Start-1, t.Len(), s.Lines)
	return Range{Start: t.Start, End: t.Start + len(s.Lines) - 1}, nil
}

// Where is an insertion position.
type Where int

const (
	Before Where = iota
	After
	AtStart
	AtEnd
)

// Insert adds Lines before or after an anchor, or at the start or end.
type Insert struct {
	Where Where
	At    anchor.Anchor // for Before and After
	Lines []string
}

func (in *Insert) Name() string { return "insert" }

func (in *Insert) Target(lines []string) (Range, error) {
	switch in.Where {
	case AtStart:
		return Range{Start: 1, End: 0}, nil
	case AtEnd:
		n := len(lines)
		return Range{Start: n + 1, End: n}, nil
	}
	r, err := anchor.Resolve(lines, in.At)
	if err != nil {
		return Range{}, err
	}
	// The anchored line is the target so version checks guard it.
	return Range{Start: r.Start, End: r.Start}, nil
}

// Addressed is the anchored line; --at-start and --at-end address no lines.
func (in *Insert) Addressed() (Range, bool) {
	if in.Where != Before && in.Where != After {
		return Range{}, false
	}
	return Range{Start: in.At.Line, End: in.At.Line}, true
}

func (in *Insert) Apply(doc *textfile.Doc, t Range) (Range, error) {
	if len(in.Lines) == 0 {
		return Range{}, outcome.New(outcome.Usage, "nothing to insert")
	}
	pos := t.Start - 1 // 0-based index to insert at
	if in.Where == After {
		pos = t.Start
	}
	doc.Replace(pos, 0, in.Lines)
	return Range{Start: pos + 1, End: pos + len(in.Lines)}, nil
}
