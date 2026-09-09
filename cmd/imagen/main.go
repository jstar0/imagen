package main

import (
	"errors"
	"fmt"
	"github.com/jstar0/imagen/internal/imagen"
	"os"
)

func main() {
	if err := imagen.NewCommand().Execute(); err != nil {
		var exit *imagen.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "imagen:", err)
		os.Exit(1)
	}
}
