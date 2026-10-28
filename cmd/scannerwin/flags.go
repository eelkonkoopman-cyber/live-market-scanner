package main

import (
	"flag"
	"fmt"
	"io"
)

// Options is the parsed command line for one shell window.
type Options struct {
	Pane Pane
	Sym  string // tape pane symbol (required); signal pane symbol (optional)
	ID   string // scan pane id
	Base string // scanner origin, e.g. http://127.0.0.1:7878
}

// DefaultBase is the scanner's fixed local origin (cmd/scanner serves UI+WS
// on 127.0.0.1:7878).
const DefaultBase = "http://127.0.0.1:7878"

// ParseArgs parses argv (without the program name) into Options. It is a
// plain func over flag.NewFlagSet so tests can drive it directly; main passes
// os.Args[1:]. Pane/sym/id consistency is validated here (delegating to
// PaneURL for the pane-specific requirements).
func ParseArgs(args []string, errOut io.Writer) (Options, error) {
	fs := flag.NewFlagSet("scannerwin", flag.ContinueOnError)
	fs.SetOutput(errOut)
	pane := fs.String("pane", string(PaneMain), "pane to open: main|tape|charts|scan|review|signal|daylog|order")
	sym := fs.String("sym", "", "symbol (required for --pane tape, optional for --pane signal)")
	id := fs.String("id", "", "scan id (required for --pane scan)")
	base := fs.String("base", DefaultBase, "scanner base URL")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return Options{}, fmt.Errorf("unexpected arguments: %v", rest)
	}
	o := Options{Pane: Pane(*pane), Sym: *sym, ID: *id, Base: *base}
	// Validate eagerly so a bad invocation fails before any window exists.
	if _, err := PaneURL(o.Base, o.Pane, o.Sym, o.ID); err != nil {
		return Options{}, err
	}
	return o, nil
}
