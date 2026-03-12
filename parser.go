package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Instruction represents a single parsed line from the Docksmithfile
type Instruction struct {
	LineNum int
	Raw     string // The exact text as written (needed for Cache Key)
	Type    string // FROM, COPY, RUN, WORKDIR, ENV, CMD
	Args    string // The arguments passed to the instruction
}

// ParseDocksmithfile reads the Docksmithfile from the given context directory and returns a slice of Instructions.
func ParseDocksmithfile(contextDir string) ([]Instruction, error) {
	filePath := contextDir + "/Docksmithfile"
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("could not open Docksmithfile: %w", err)
	}
	defer file.Close()

	var instructions []Instruction
	scanner := bufio.NewScanner(file)
	lineNum := 0

	validInstructions := map[string]bool{
		"FROM":    true,
		"COPY":    true,
		"RUN":     true,
		"WORKDIR": true,
		"ENV":     true,
		"CMD":     true,
	}

	for scanner.Scan() {
		lineNum++
		rawLine := scanner.Text()
		trimmedLine := strings.TrimSpace(rawLine)

		// Skip empty lines and comments
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
			continue
		}

		// Split the instruction from its arguments
		splitIdx := strings.IndexAny(trimmedLine, " \t[")
		var instrType string
		var rawArgs string

		if splitIdx == -1 {
			instrType = strings.ToUpper(trimmedLine)
		} else {
			instrType = strings.ToUpper(trimmedLine[:splitIdx])
			rawArgs = trimmedLine[splitIdx:]
		}

		// failure detection for unrecognized instructions
		if !validInstructions[instrType] {
			return nil, fmt.Errorf("error on line %d: unrecognized instruction '%s'", lineNum, instrType)
		}

		args := strings.TrimSpace(rawArgs)

		instructions = append(instructions, Instruction{
			LineNum: lineNum,
			Raw:     trimmedLine,
			Type:    instrType,
			Args:    args,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	if len(instructions) == 0 {
		return nil, fmt.Errorf("Docksmithfile is empty")
	}

	// The first instruction MUST be FROM
	if instructions[0].Type != "FROM" {
		return nil, fmt.Errorf("error on line %d: Docksmithfile must start with a FROM instruction", instructions[0].LineNum)
	}

	return instructions, nil
}
