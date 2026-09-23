package tokens

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// C collects the failures of one check.
type C struct {
	root     string
	pristine Manifest
	errs     []string
}

// Errorf records a failure.
func (c *C) Errorf(format string, args ...any) { c.errs = append(c.errs, fmt.Sprintf(format, args...)) }

func (c *C) path(rel string) string { return filepath.Join(c.root, filepath.FromSlash(rel)) }

// Read returns the content of rel, recording a failure if it is missing.
func (c *C) Read(rel string) (string, bool) {
	b, err := os.ReadFile(c.path(rel))
	if err != nil {
		c.Errorf("%s: %v", rel, err)
		return "", false
	}
	return string(b), true
}

// Contains wants every s in rel.
func (c *C) Contains(rel string, ss ...string) {
	src, ok := c.Read(rel)
	for _, s := range ss {
		if ok && !strings.Contains(src, s) {
			c.Errorf("%s does not contain %q", rel, s)
		}
	}
}

// Lacks wants none of ss in rel.
func (c *C) Lacks(rel string, ss ...string) {
	src, ok := c.Read(rel)
	for _, s := range ss {
		if ok && strings.Contains(src, s) {
			c.Errorf("%s still contains %q", rel, s)
		}
	}
}

// Missing wants rel not to exist.
func (c *C) Missing(rel string) {
	if _, err := os.Stat(c.path(rel)); err == nil {
		c.Errorf("%s still exists", rel)
	}
}

// SameAs wants rel to have exactly the pristine content of orig.
func (c *C) SameAs(rel, orig string) {
	b, err := os.ReadFile(c.path(rel))
	if err != nil {
		c.Errorf("%s: %v", rel, err)
		return
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != c.pristine[orig] {
		c.Errorf("%s is not an unchanged copy of %s", rel, orig)
	}
}

// Parse parses the Go file rel.
func (c *C) Parse(rel string) (*token.FileSet, *ast.File) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, c.path(rel), nil, parser.ParseComments)
	if err != nil {
		c.Errorf("%s does not parse: %v", rel, err)
		return fset, nil
	}
	return fset, f
}

// Words counts whole-word matches of word in files under dir ("" is the
// root) whose names end in ext.
func (c *C) Words(dir, ext, word string) int {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
	n := 0
	_ = filepath.WalkDir(c.path(dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ext) {
			return nil
		}
		if rel, _ := filepath.Rel(c.root, p); ignored(filepath.ToSlash(rel)) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil {
			n += len(re.FindAll(b, -1))
		}
		return nil
	})
	return n
}

// WantWords wants exactly n whole-word matches of word in .go files under dir.
func (c *C) WantWords(dir, word string, n int) {
	if got := c.Words(dir, ".go", word); got != n {
		c.Errorf("%d occurrences of %s under %s/, want %d", got, word, dir, n)
	}
}

var durations = strings.NewReplacer(
	"time.Nanosecond", "(1)", "time.Microsecond", "(1e3)", "time.Millisecond", "(1e6)",
	"time.Second", "(1e9)", "time.Minute", "(6e10)", "time.Hour", "(3.6e12)",
)

// WantConst wants the value of the package-level const or var name in rel
// to evaluate to want. time.Duration units are understood.
func (c *C) WantConst(rel, name string, want int64) {
	fset, f := c.Parse(rel)
	if f == nil {
		return
	}
	var expr ast.Expr
	ast.Inspect(f, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok {
			for i, id := range vs.Names {
				if id.Name == name && i < len(vs.Values) {
					expr = vs.Values[i]
				}
			}
		}
		return expr == nil
	})
	if expr == nil {
		c.Errorf("%s: no value for %s", rel, name)
		return
	}
	var b bytes.Buffer
	_ = printer.Fprint(&b, fset, expr)
	src := durations.Replace(b.String())
	tv, err := types.Eval(token.NewFileSet(), nil, token.NoPos, src)
	if err != nil || tv.Value == nil {
		c.Errorf("%s: cannot evaluate %s = %s: %v", rel, name, b.String(), err)
		return
	}
	v := constant.ToInt(tv.Value)
	if v.Kind() != constant.Int || !constant.Compare(v, token.EQL, constant.MakeInt64(want)) {
		c.Errorf("%s: %s = %s, want %d", rel, name, b.String(), want)
	}
}

// Func finds the function or method name (recv "" for functions, else the
// receiver type name without *) in f, and its index in f.Decls.
func Func(f *ast.File, recv, name string) (*ast.FuncDecl, int) {
	for i, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name {
			continue
		}
		if recvName(fd) == recv {
			return fd, i
		}
	}
	return nil, -1
}

func recvName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

// Signature renders fd's type without the func keyword and names, e.g.
// "(string) (int, error)".
func Signature(fd *ast.FuncDecl) string {
	list := func(fl *ast.FieldList) []string {
		var out []string
		if fl == nil {
			return out
		}
		for _, f := range fl.List {
			var b bytes.Buffer
			_ = printer.Fprint(&b, token.NewFileSet(), f.Type)
			n := len(f.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				out = append(out, b.String())
			}
		}
		return out
	}
	s := "(" + strings.Join(list(fd.Type.Params), ", ") + ")"
	switch res := list(fd.Type.Results); len(res) {
	case 0:
	case 1:
		s += " " + res[0]
	default:
		s += " (" + strings.Join(res, ", ") + ")"
	}
	return s
}

// Uses reports whether node mentions the identifier name.
func Uses(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// HasNewline reports whether node contains a '\n' or "\n" literal.
func HasNewline(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if bl, ok := n.(*ast.BasicLit); ok && (bl.Value == `'\n'` || strings.Contains(bl.Value, `\n`)) {
			found = true
		}
		return !found
	})
	return found
}

// parseChanged checks that every changed or added Go file still parses.
func (c *C) parseChanged(changed []string) {
	for _, p := range changed {
		if strings.HasSuffix(p, ".go") {
			if _, err := os.Stat(c.path(p)); err == nil {
				c.Parse(p)
			}
		}
	}
}
