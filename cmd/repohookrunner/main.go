package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"repospanner.org/repospanner/hookrun"
	"repospanner.org/repospanner/server/constants"
)

func main() {
	if !constants.VersionBuiltIn() {
		fmt.Fprintf(os.Stderr, "Invalid build")
		err = errors.New("Invalid build")
		return
	}
	if len(os.Args) != 2 {
		os.Fprintln(os.Stderr, "Invalid call to repoSpanner hookrunner: need more arguments")
		os.Exit(1)
	}
	if os.Args[1] == "--version" {
		fmt.Println("repoSpanner Hook Runner " + constants.PublicVersionString())
		os.Exit(0)
	}
	if !strings.HasPrefix(os.Args[1], "--hookrun-api") {
		fmt.Fprintln(os.Stderr, "Invalid call to repoSpanner hookrunner: too old version")
		os.Exit(1)
	}

	controlfile := os.NewFile(3, "controlfile")
	if controlfile == nil {
		panic("Unable to open control file")
	}
	err := hookrun.Run(controlfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running hook protocol: %s", err)
		os.Exit(1)
	}
}
