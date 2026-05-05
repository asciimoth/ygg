package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

const sourceURL = "https://asciimoth.github.io/yggpeers/peers.json"

func main() {
	output := flag.String("output", "", "output file")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "missing -output")
		os.Exit(2)
	}

	resp, err := http.Get(sourceURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch builtin peers: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "fetch builtin peers: unexpected status %s\n", resp.Status)
		os.Exit(1)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read builtin peers: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*output, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write builtin peers: %v\n", err)
		os.Exit(1)
	}
}
