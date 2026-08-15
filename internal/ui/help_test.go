package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestLeafHelpOmitsSubcommandFooter(t *testing.T) {
	root := &cobra.Command{Use: "axis"}
	leaf := &cobra.Command{
		Use:   "doctor",
		Short: "Validate configuration",
		Run:   func(cmd *cobra.Command, args []string) {},
	}
	root.AddCommand(leaf)
	ApplyHelpTemplate(root)

	var buf bytes.Buffer
	leaf.SetOut(&buf)
	if err := leaf.Help(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "doctor <command> --help") {
		t.Fatalf("leaf help must not invent subcommands:\n%s", got)
	}
}

func TestParentHelpKeepsSubcommandFooter(t *testing.T) {
	root := &cobra.Command{Use: "axis"}
	parent := &cobra.Command{Use: "cluster", Short: "See the cluster"}
	parent.AddCommand(&cobra.Command{Use: "status", Run: func(cmd *cobra.Command, args []string) {}})
	root.AddCommand(parent)
	ApplyHelpTemplate(root)

	var buf bytes.Buffer
	parent.SetOut(&buf)
	if err := parent.Help(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "cluster <command> --help") {
		t.Fatalf("parent help must keep subcommand footer:\n%s", buf.String())
	}
}
