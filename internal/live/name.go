package live

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/output"
	"github.com/astralyx/lino/internal/registry"
)

// nameFile holds the alias a root's process runs under, so a later start
// (plain `lino run` or an auto-start) keeps it.
const nameFile = "name"

func namePath(root string) string { return filepath.Join(root, ".lino", nameFile) }

// StoredName returns the alias saved for root, or "" when none is saved or
// the saved one is not a valid name.
func StoredName(root string) string {
	b, err := os.ReadFile(namePath(root))
	if err != nil {
		return ""
	}
	n := strings.TrimSpace(string(b))
	if !registry.ValidName(n) {
		return ""
	}
	return n
}

// saveName records name as the alias of root.
func saveName(root, name string) error {
	if StoredName(root) == name {
		return nil
	}
	tmp := namePath(root) + ".tmp"
	if err := os.WriteFile(tmp, []byte(name+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, namePath(root))
}

// Rename gives the live process of root the alias name: the registry entry is
// rewritten (refused when another live process holds the name) and the name is
// saved for later starts.
func Rename(reg *registry.Registry, root, name string) (registry.Entry, error) {
	if !registry.ValidName(name) {
		return registry.Entry{}, outcome.New(outcome.Usage, "invalid name %q: use letters, digits, '.', '_' or '-'", name)
	}
	e, err := reg.Read(registry.IDFor(root))
	if err != nil {
		return registry.Entry{}, err
	}
	if e.Name == name {
		return e, saveName(root, name)
	}
	e.Name = name
	if err := reg.Write(e); err != nil {
		return registry.Entry{}, err
	}
	if err := saveName(root, name); err != nil {
		return registry.Entry{}, outcome.Wrap(outcome.Internal, err, "save name: "+err.Error())
	}
	return e, nil
}

// alreadyRunning reports the live process d; with a name it is renamed first.
func alreadyRunning(reg *registry.Registry, d RunData, name string) (output.Result, error) {
	if name == "" {
		return output.Result{Data: d, Message: "already running"}, nil
	}
	e, err := Rename(reg, d.Root, name)
	if err != nil {
		return output.Result{}, err
	}
	d.Name = e.Name
	return output.Result{Data: d, Message: fmt.Sprintf("already running (named %s)", name)}, nil
}

// CurrentName returns the alias in p's registry entry, which `run --name` may
// have changed since the start; p.Name when the entry cannot be read.
func (p *Process) CurrentName() string {
	if e, err := p.reg.Read(p.ID); err == nil && e.PID == os.Getpid() {
		return e.Name
	}
	return p.Name
}
