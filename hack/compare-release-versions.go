package main

import (
	"fmt"
	"os"

	"github.com/toasterbook88/axis/internal/versioncmp"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run ./hack/compare-release-versions.go <current> <latest>")
		os.Exit(2)
	}

	cmp, err := versioncmp.Compare(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	switch cmp {
	case -1:
		fmt.Println("behind")
	case 0:
		fmt.Println("equal")
	case 1:
		fmt.Println("ahead")
	default:
		fmt.Fprintf(os.Stderr, "unexpected comparison result: %d\n", cmp)
		os.Exit(1)
	}
}
