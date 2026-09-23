// Package commands links every lino command into cli.Default. The lino-core
// binary and tests that need the full command set import it for its side
// effects.
package commands

import (
	_ "github.com/astralyx/lino/internal/changelog"
	_ "github.com/astralyx/lino/internal/changescmd"
	_ "github.com/astralyx/lino/internal/filecmd"
	_ "github.com/astralyx/lino/internal/helpcmd"
	_ "github.com/astralyx/lino/internal/historycmd"
	_ "github.com/astralyx/lino/internal/histprune"
	_ "github.com/astralyx/lino/internal/histrec"
	_ "github.com/astralyx/lino/internal/initcmd"
	_ "github.com/astralyx/lino/internal/live"
	_ "github.com/astralyx/lino/internal/lscmd"
	_ "github.com/astralyx/lino/internal/metrics"
	_ "github.com/astralyx/lino/internal/pscmd"
	_ "github.com/astralyx/lino/internal/reindex"
	_ "github.com/astralyx/lino/internal/rollback"
	_ "github.com/astralyx/lino/internal/search"
	_ "github.com/astralyx/lino/internal/showcmd"
	_ "github.com/astralyx/lino/internal/statscmd"
	_ "github.com/astralyx/lino/internal/statuscmd"
	_ "github.com/astralyx/lino/internal/stopcmd"
	_ "github.com/astralyx/lino/internal/vcheck"
)
