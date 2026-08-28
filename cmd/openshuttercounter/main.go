package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mvisonneau/openshuttercounter/internal/cli"
)

func main() {
	if err := cli.NewApp().Run(context.Background(), os.Args); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}
