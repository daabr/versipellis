package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/daabr/versipellis/pkg/config"
)

func flags(info *debug.BuildInfo) (exit, debugLog, structured bool, path string) {
	var help, version bool
	var opts strings.Builder

	usage := "Print command-line usage information and exit"
	fmt.Fprintf(&opts, "   --help, -h, -?  %s\n", usage)
	flag.BoolVar(&help, "help", false, usage)
	flag.BoolVar(&help, "h", false, usage)
	flag.BoolVar(&help, "?", false, usage)

	usage = "Print app version information and exit"
	fmt.Fprintf(&opts, "   --version, -v   %s\n\n", usage)
	flag.BoolVar(&version, "version", false, usage)
	flag.BoolVar(&version, "v", false, usage)

	usage = "Log debug-level messages and above (default: info-level and above)"
	fmt.Fprintf(&opts, "   --debug, -d     %s\n", usage)
	flag.BoolVar(&debugLog, "debug", false, usage)
	flag.BoolVar(&debugLog, "d", false, usage)

	usage = "Output logs to STDERR in JSON format (default: STDOUT with ANSI colors)"
	fmt.Fprintf(&opts, "   --json, -j      %s\n\n", usage)
	flag.BoolVar(&structured, "json", false, usage)
	flag.BoolVar(&structured, "j", false, usage)

	usage = fmt.Sprintf(`Relative/absolute path to TOML configuration file (default: %q)`, config.DefaultFilePath)
	fmt.Fprintf(&opts, "   --config, -c    %s\n", usage)
	flag.StringVar(&path, "config", config.DefaultFilePath, usage)
	flag.StringVar(&path, "c", config.DefaultFilePath, usage)

	flag.Parse()
	switch {
	case help:
		printHelp(opts.String())
	case version:
		fmt.Printf("Versipellis version: %s\n", info.Main.Version)
	}

	return help || version, debugLog, structured, path
}

// Nicer than the default text in [flag.Usage].
func printHelp(opts string) {
	var help strings.Builder
	help.WriteString("\nVersipellis - data flow shape shifter\n\n")
	fmt.Fprintf(&help, "USAGE:\n   %s [OPTIONS]\n\n", filepath.Base(os.Args[0]))
	fmt.Fprintf(&help, "OPTIONS:\n%s", opts)
	fmt.Println(help.String())
}
