package app

import (
	"fmt"
	"io"

	"github.com/alecthomas/kong"
)

const banner = `██████╗  ██████╗ ██╗   ██╗████████╗███████╗███╗   ███╗███████╗███████╗██╗  ██╗
██╔══██╗██╔═══██╗██║   ██║╚══██╔══╝██╔════╝████╗ ████║██╔════╝██╔════╝██║  ██║
██████╔╝██║   ██║██║   ██║   ██║   █████╗  ██╔████╔██║█████╗  ███████╗███████║
██╔══██╗██║   ██║██║   ██║   ██║   ██╔══╝  ██║╚██╔╝██║██╔══╝  ╚════██║██╔══██║
██║  ██║╚██████╔╝╚██████╔╝   ██║   ███████╗██║ ╚═╝ ██║███████╗███████║██║  ██║
╚═╝  ╚═╝ ╚═════╝  ╚═════╝    ╚═╝   ╚══════╝╚═╝     ╚═╝╚══════╝╚══════╝╚═╝  ╚═╝
`

func writeBanner(w io.Writer) {
	fmt.Fprint(w, banner)
}

// helpPrinter prepends the RouteMesh banner to the top-level --help output,
// then delegates to kong's default printer for the usage text itself.
func helpPrinter(options kong.HelpOptions, ctx *kong.Context) error {
	if ctx.Selected() == nil {
		writeBanner(ctx.Stdout)
		fmt.Fprintln(ctx.Stdout)
	}
	return kong.DefaultHelpPrinter(options, ctx)
}
