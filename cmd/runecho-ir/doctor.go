package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/inth3shadows/runecho/internal/doctor"
)

// runDoctor answers "is this install actually wired and answering?" (#331).
// It is read-only — it fixes nothing and installs nothing, purely a report —
// so the exit code carries no meaning about the checks themselves unless
// --strict is passed; see doctor.Run's doc comment for what each check covers
// and why no single existing command already answers this.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable JSON")
	strict := fs.Bool("strict", false, "exit 2 if any check is warn or fail (default: always exit 0)")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}

	results := doctor.Run(root)

	if *asJSON {
		out, err := json.MarshalIndent(map[string]any{"root": root, "checks": results}, "", "  ")
		if err != nil {
			return printErr(err)
		}
		fmt.Println(string(out))
	} else {
		printDoctorReport(root, results)
	}

	if *strict {
		for _, r := range results {
			if r.Status != doctor.OK {
				return ExitError
			}
		}
	}
	return ExitOK
}

func printDoctorReport(root string, results []doctor.Result) {
	fmt.Printf("runecho-ir doctor — %s\n\n", root)
	for _, r := range results {
		fmt.Printf("[%s] %-28s %s\n", statusGlyph(r.Status), r.Check, r.Detail)
		if r.Remedy != "" {
			fmt.Printf("      -> %s\n", r.Remedy)
		}
	}
	fmt.Fprintln(os.Stdout)
}

func statusGlyph(s doctor.Status) string {
	switch s {
	case doctor.OK:
		return " ok "
	case doctor.Warn:
		return "warn"
	case doctor.Fail:
		return "fail"
	default:
		return "????"
	}
}
