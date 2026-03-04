package main

import (
	"fmt"
	"io"
	"os"

	"github.com/inth3shadows/runecho/internal/governor"
)

func main() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(0) // never block the session
	}

	apiKey := os.Getenv("RUNECHO_CLASSIFIER_KEY")
	output := governor.Run(input, apiKey)
	if output != "" {
		fmt.Print(output)
	}
}
