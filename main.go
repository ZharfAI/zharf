// zharf is the command-line tool for platform.zharf.ai: every model on the platform from your
// terminal, served locally over the Ollama and OpenAI APIs.
package main

import (
	"os"

	"github.com/ZharfAI/zharf/cmd"
)

func main() {
	os.Exit(cmd.Main(os.Args[1:]))
}
