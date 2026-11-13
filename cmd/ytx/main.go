// Command ytx is YouTube for agents: keyless research, the official API when a
// key is set, and your own channel and analytics after `ytx login`.
package main

import (
	"os"

	"github.com/morethancoder/ytx/internal/cli"
	// Areas attach their handlers to the ops table in init().
	_ "github.com/morethancoder/ytx/internal/api"       // official Data API v3 reader
	_ "github.com/morethancoder/ytx/internal/channel"   // login, channel writes, analytics
	_ "github.com/morethancoder/ytx/internal/download"  // yt-dlp downloads
	_ "github.com/morethancoder/ytx/internal/feedcheck" // feed preview sheets for agd
	_ "github.com/morethancoder/ytx/internal/keyless"   // InnerTube reader
	_ "github.com/morethancoder/ytx/internal/reads"
	_ "github.com/morethancoder/ytx/internal/research" // outliers, gaps, research notes
)

func main() { os.Exit(cli.Execute()) }
