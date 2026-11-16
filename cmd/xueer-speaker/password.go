package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// readPassword reads a line without echo when stdin is a terminal, and a
// plain line otherwise (piped input in scripts and tests).
func readPassword(prompt string, r *bufio.Reader) string {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil {
			return string(b)
		}
	}
	s, _ := r.ReadString('\n')
	return strings.TrimRight(s, "\r\n")
}
