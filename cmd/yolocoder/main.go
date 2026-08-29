package main

import (
	"fmt"
	"os"

	"github.com/mindsdb/yolocoder/internal/update"
	"github.com/mindsdb/yolocoder/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("yolocoder %s\n", version.Display())
			return
		case "help", "--help", "-h":
			fmt.Println("Usage: yolocoder [version]")
			return
		}
	}

	update.CheckOnLaunch(version.Commit)
	fmt.Println("Welcome to YoloCode CLI")
}
