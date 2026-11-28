package main

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// agentFlagError marks a flag error Cobra raised for `axis agent` while
// parsing flags, before RunE ran. The RunE defer that writes the
// -p --format json failure document never sees such an error, so
// executeRoot writes the document for it.
type agentFlagError struct{ err error }

func (e agentFlagError) Error() string { return e.err.Error() }
func (e agentFlagError) Unwrap() error { return e.err }

// executeRoot runs root with args. When `axis agent -p --format json` fails
// on a flag Cobra could not parse (a malformed value or an unknown flag), it
// writes the failed axis.output/v1 document that RunE would have written, so
// a caller reading stdout gets one document for every outcome.
func executeRoot(root *cobra.Command, args []string) error {
	root.SetArgs(args)
	cmd, err := root.ExecuteC()
	var fe agentFlagError
	if errors.As(err, &fe) && cmd != nil && pipedJSONRequested(cmd, args) {
		writePipedStartupFailure(cmd.OutOrStdout(), fe.err, parsedMaxTurns(cmd))
	}
	return err
}

// parsedMaxTurns is --max-turns as parsed, or its default when it was not
// parsed: pflag stores the zero value when a malformed value fails to parse,
// so "--max-turns nope" would otherwise report 0.
func parsedMaxTurns(cmd *cobra.Command) int {
	f := cmd.Flags().Lookup("max-turns")
	if f == nil {
		return 0
	}
	v := f.DefValue
	if f.Changed {
		v = f.Value.String()
	}
	n, _ := strconv.Atoi(v)
	return n
}

// pipedJSONRequested reports whether args ask the agent command for -p and
// --format json. Flag parsing stopped at an error, so it reads the raw args:
// in order up to "--" (a later --format or --print wins), skipping the value
// of each flag that takes one so a value such as `--model -p` is not read as
// a flag. Unknown flags are skipped on their own.
func pipedJSONRequested(cmd *cobra.Command, args []string) bool {
	lookup := func(name string) *pflag.Flag {
		if f := cmd.Flags().Lookup(name); f != nil {
			return f
		}
		return cmd.InheritedFlags().Lookup(name)
	}
	shorthand := func(s string) *pflag.Flag {
		if f := cmd.Flags().ShorthandLookup(s); f != nil {
			return f
		}
		return cmd.InheritedFlags().ShorthandLookup(s)
	}
	takesValue := func(f *pflag.Flag) bool { return f.NoOptDefVal == "" }
	isTrue := func(v string) bool { b, err := strconv.ParseBool(v); return err == nil && b }

	printMode, format := false, "text"
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		if strings.HasPrefix(a, "--") {
			name, val, hasVal := strings.Cut(a[2:], "=")
			f := lookup(name)
			if f == nil {
				continue
			}
			if !hasVal && takesValue(f) && i+1 < len(args) {
				i++
				val, hasVal = args[i], true
			}
			switch f.Name {
			case "print":
				printMode = !hasVal || isTrue(val)
			case "format":
				format = val
			}
			continue
		}
		// A cluster of shorthands, such as -sp or -mqwen.
		sh := a[1:]
		for j := 0; j < len(sh); j++ {
			f := shorthand(sh[j : j+1])
			if f == nil {
				continue // unknown: still look for -p in the rest, as in -xp
			}
			if takesValue(f) {
				if j+1 == len(sh) && i+1 < len(args) {
					i++
				}
				break
			}
			if f.Name == "print" {
				if rest := sh[j+1:]; strings.HasPrefix(rest, "=") {
					printMode = isTrue(rest[1:])
					break
				}
				printMode = true
			}
		}
	}
	// RunE normalizes --format the same way (validateOutputFormat).
	return printMode && strings.ToLower(strings.TrimSpace(format)) == "json"
}
