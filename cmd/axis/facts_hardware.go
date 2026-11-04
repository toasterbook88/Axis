package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/ui"
)

// normalizeHardwareArgs translates the exact, documented -hw shortcut before
// Cobra parses -h. It only applies to facts, respects -- and flag values, and
// leaves all other commands and shorthand combinations untouched.
func normalizeHardwareArgs(args []string) []string {
	result := append([]string{}, args...)
	command := []string{}
	inFacts := false
	skipValue := false
	for i, arg := range result {
		if skipValue {
			skipValue = false
			continue
		}
		if arg == "--" {
			break
		}
		if inFacts {
			if arg == "--format" || arg == "--interval" {
				skipValue = true
				continue
			}
			if arg == "-hw" {
				result[i] = "--hardware"
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		command = append(command, arg)
		if len(command) == 1 && arg == "facts" {
			inFacts = true
			continue
		}
		if len(command) == 1 && arg == "node" {
			continue
		}
		if len(command) == 2 && command[0] == "node" && arg == "facts" {
			inFacts = true
			continue
		}
		break
	}
	return result
}

type hardwareSampler interface {
	Sample(context.Context, time.Duration) (*models.HardwareReport, error)
}

var newHardwareSampler = func() hardwareSampler { return facts.NewHardwareCollector() }

func runHardwareFacts(cmd *cobra.Command, format string, watch bool, interval time.Duration) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	collector := newHardwareSampler()
	for {
		frameCtx, cancel := context.WithTimeout(ctx, interval+30*time.Second)
		report, err := collector.Sample(frameCtx, interval)
		cancel()
		if err != nil {
			if watch && ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch {
		case format == "text":
			err = printHardwareFacts(cmd.OutOrStdout(), report)
		case watch && format == "json":
			err = json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		default:
			if watch && format == "yaml" {
				if _, err = fmt.Fprintln(cmd.OutOrStdout(), "---"); err != nil {
					return err
				}
			}
			err = printOutput(cmd.OutOrStdout(), report, format)
		}
		if err != nil {
			return err
		}
		if !watch {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

func printHardwareFacts(out io.Writer, r *models.HardwareReport) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", ui.Bold("HARDWARE FACTS"), ui.Cyan(r.Hostname))
	fmt.Fprintf(&b, "  %-22s %s / %s\n", "platform:", r.OS, r.Arch)
	fmt.Fprintf(&b, "  %-22s %s\n", "collected:", r.CollectedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "  %-22s %s\n", "inventory:", r.InventoryAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "  %-22s %s\n", "coverage:", r.Status)
	fmt.Fprintf(&b, "  %-22s %.3f s (%s to %s)\n", "counter sample:", r.SampleSeconds, r.SampleStart.Format(time.RFC3339Nano), r.SampleEnd.Format(time.RFC3339Nano))
	unavailable := 0
	for _, s := range r.Sections {
		fmt.Fprintf(&b, "\n%s\n", ui.Bold(s.Name))
		for _, d := range s.Devices {
			fmt.Fprintf(&b, "  %s [%s]\n", ui.Bold(ui.StripANSIAndControls(d.Name)), ui.StripANSIAndControls(d.ID))
			for _, group := range []struct {
				name   string
				values []models.HardwareReading
			}{{"Specifications", d.Specs}, {"Live measurements", d.Metrics}} {
				if len(group.values) == 0 {
					continue
				}
				fmt.Fprintf(&b, "    %s\n", group.name)
				for _, v := range group.values {
					name := ui.StripANSIAndControls(v.Name)
					source := ui.StripANSIAndControls(v.Source)
					if v.SampleSeconds > 0 {
						source = fmt.Sprintf("%s; sample %.3f s", source, v.SampleSeconds)
					}
					if v.Status != "observed" && v.Status != "derived" {
						unavailable++
						fmt.Fprintf(&b, "      %-28s unavailable (%s: %s) [%s]\n", name, v.Status, ui.StripANSIAndControls(v.Reason), source)
						continue
					}
					value := fmt.Sprint(v.Value)
					if n, ok := v.Value.(float64); ok {
						value = fmt.Sprintf("%.2f", n)
					}
					fmt.Fprintf(&b, "      %-28s %s %s [%s]\n", name, ui.StripANSIAndControls(value), v.Unit, source)
				}
			}
		}
	}
	fmt.Fprintf(&b, "\nCoverage: %d unavailable readings; reasons shown alongside each field.\n", unavailable)
	_, err := io.WriteString(out, b.String())
	return err
}
