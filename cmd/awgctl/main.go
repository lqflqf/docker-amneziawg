// Command awgctl manages the AmneziaWG server in this container: the SQLite
// database in /config, the rendered confs in /run, peers and AWG parameters.
package main

import (
	"os"

	"github.com/lqflqf/docker-amneziawg/internal/cli"
)

func main() {
	os.Exit(cli.New().Main(os.Args[1:]))
}
