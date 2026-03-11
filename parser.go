package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Instruction represents a single parsed line from a Docksmithfile.
type Instruction struct {
	Line int
	Op   string // always upper-case
	Args string
}

var validOps = map[string]bool{
	"FROM": true, "COPY": true, "RUN": true,
	"WORKDIR": true, "ENV": true, "CMD": true,
}

// ParseDocksmithfile parses a Docksmithfile and returns the instruction list.
// Blank lines and lines starting with # are ignored.
// Any unrecognised instruction causes an immediate error with the line number.
func ParseDocksmithfile(path string) ([]Instruction, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Docksmithfile: %w", err)
	}
	defer f.Close()

	var instructions []Instruction
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)

		// Skip blank lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split "OP args..."
		parts := strings.SplitN(line, " ", 2)
		op := strings.ToUpper(parts[0])
		args := ""
		if len(parts) == 2 {
			args = strings.TrimSpace(parts[1])
		}

		if !validOps[op] {
			return nil, fmt.Errorf("line %d: unrecognised instruction %q", lineNum, parts[0])
		}

		instructions = append(instructions, Instruction{
			Line: lineNum,
			Op:   op,
			Args: args,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Docksmithfile: %w", err)
	}

	return instructions, nil
}