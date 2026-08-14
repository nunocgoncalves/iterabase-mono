package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/catalogue"
)

func main() {
	format := flag.String("format", "json", "output format: json or markdown")
	rootFlag := flag.String("root", "", "repository root (defaults to discovery from cwd)")
	output := flag.String("output", "", "output path (defaults to stdout)")
	flag.Parse()

	root := *rootFlag
	var err error
	if root == "" {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			fail(cwdErr)
		}
		root, err = catalogue.FindRoot(cwd)
		if err != nil {
			fail(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	compiled, err := catalogue.Discover(ctx, root)
	if err != nil {
		fail(err)
	}
	var data []byte
	switch *format {
	case "json":
		data, err = compiled.JSON()
	case "markdown", "md":
		data = compiled.Markdown()
	default:
		fail(fmt.Errorf("unsupported format %q", *format))
	}
	if err != nil {
		fail(err)
	}
	if *output == "" {
		_, err = os.Stdout.Write(data)
	} else {
		path, pathErr := filepath.Abs(*output)
		if pathErr != nil {
			fail(pathErr)
		}
		err = os.WriteFile(path, data, 0o600)
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "e2e catalogue: %v\n", err)
	os.Exit(1)
}
