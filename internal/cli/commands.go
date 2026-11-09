package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
)

// opts holds the flag values of one command. Each command registers only
// the flags that it accepts, so another flag is a usage error.
type opts struct {
	yes, showSecrets, noKeepalive, qr, json, clear, dryRun, forcePartial bool
	name, png, conf, keepalive                                           string
	rename                                                               renameFlag
}

// flagDefs registers each flag that a command can accept.
var flagDefs = map[string]func(*flag.FlagSet, *opts){
	"yes":           func(fs *flag.FlagSet, o *opts) { fs.BoolVar(&o.yes, "yes", false, "") },
	"show-secrets":  func(fs *flag.FlagSet, o *opts) { fs.BoolVar(&o.showSecrets, "show-secrets", false, "") },
	"no-keepalive":  func(fs *flag.FlagSet, o *opts) { fs.BoolVar(&o.noKeepalive, "no-keepalive", false, "") },
	"qr":            func(fs *flag.FlagSet, o *opts) { fs.BoolVar(&o.qr, "qr", false, "") },
	"json":          func(fs *flag.FlagSet, o *opts) { fs.BoolVar(&o.json, "json", false, "") },
	"clear":         func(fs *flag.FlagSet, o *opts) { fs.BoolVar(&o.clear, "clear", false, "") },
	"dry-run":       func(fs *flag.FlagSet, o *opts) { fs.BoolVar(&o.dryRun, "dry-run", false, "") },
	"force-partial": func(fs *flag.FlagSet, o *opts) { fs.BoolVar(&o.forcePartial, "force-partial", false, "") },
	"name":          func(fs *flag.FlagSet, o *opts) { fs.StringVar(&o.name, "name", "", "") },
	"png":           func(fs *flag.FlagSet, o *opts) { fs.StringVar(&o.png, "png", "", "") },
	"conf":          func(fs *flag.FlagSet, o *opts) { fs.StringVar(&o.conf, "conf", "", "") },
	"keepalive":     func(fs *flag.FlagSet, o *opts) { fs.StringVar(&o.keepalive, "keepalive", "", "") },
	"rename": func(fs *flag.FlagSet, o *opts) {
		o.rename = renameFlag{}
		fs.Var(o.rename, "rename", "")
	},
}

// command is one awgctl command or subcommand.
type command struct {
	name  string   // "peer add"
	args  string   // the arguments and flags in the usage text
	help  string   // a description in the usage text ("" = none)
	flags []string // the accepted flags (keys of flagDefs)
	min   int      // the minimum number of positional arguments
	max   int      // the maximum number of positional arguments (-1 = no limit)
	run   func(*App, context.Context, *call) error
}

// call is one parsed command line.
type call struct {
	opts
	cmd *command
	pos []string
}

func (c *command) line() string { return strings.TrimSpace(c.name + " " + c.args) }

func (c *command) usageErr() error { return usagef("usage: awgctl %s", c.line()) }

// parse parses the flags and checks the number of positional arguments.
func (c *command) parse(args []string) (*call, error) {
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	cl := &call{cmd: c}
	for _, f := range c.flags {
		flagDefs[f](fs, &cl.opts)
	}
	pos, err := parse(fs, args)
	if err != nil {
		return nil, err
	}
	if len(pos) < c.min || c.max >= 0 && len(pos) > c.max {
		return nil, c.usageErr()
	}
	cl.pos = pos
	return cl, nil
}

type group struct {
	title string
	cmds  []command
}

// groups are the commands in usage order.
var groups = []group{
	{"System", []command{
		{name: "init", help: "s6 start-up: create or check the DB, render /run", run: (*App).cmdInit},
		{name: "health", help: "Docker health check", run: (*App).cmdHealth},
		{name: "version", help: "versions of awgctl, awg and amneziawg-go", run: (*App).cmdVersion},
	}},
	{"Settings (not AWG)", []command{
		{name: "get", args: "[key] [--show-secrets]", flags: []string{"show-secrets"}, max: 1, run: (*App).cmdGet},
		{name: "set", args: "<key> <value> [--yes]", flags: []string{"yes"}, min: 2, max: 2, run: (*App).cmdSet},
		{name: "reset", args: "<key> [--yes]", flags: []string{"yes"}, min: 1, max: 1, run: (*App).cmdReset},
		{name: "server rotate-key", args: "--yes", flags: []string{"yes"}, run: (*App).cmdServerRotateKey},
	}},
	{"AmneziaWG parameters", []command{
		{name: "awg show", args: "[--show-secrets]", flags: []string{"show-secrets"}, run: (*App).awgShow},
		{name: "awg version", args: "<" + strings.Join(awg.Versions, "|") + "> --yes", flags: []string{"yes"},
			min: 1, max: 1, run: (*App).awgVersion},
		{name: "awg set", args: "<key>=<value>... [--yes]", help: "(s=<n> sets S1-S4 together)",
			flags: []string{"yes"}, min: 1, max: -1, run: (*App).awgSet},
		{name: "awg regenerate", args: "[keys...] --yes", flags: []string{"yes"}, max: -1, run: (*App).awgRegenerate},
	}},
	{"Peers (<id> = the peer id, a number 2-254, also the last octet of its address)", []command{
		{name: "peer add", args: "[--name NAME] [--no-keepalive] [--qr]", flags: []string{"name", "no-keepalive", "qr"},
			run: (*App).peerAdd},
		{name: "peer add-many", args: "<count> [--no-keepalive]", flags: []string{"no-keepalive"},
			min: 1, max: 1, run: (*App).peerAddMany},
		{name: "peer list", args: "[--json]", flags: []string{"json"}, run: (*App).peerList},
		{name: "peer show", args: "<id>", min: 1, max: 1, run: (*App).peerShow},
		// --qr, --png and --conf can be used together.
		{name: "peer export", args: "<id> [--qr] [--png FILE] [--conf FILE]", flags: []string{"qr", "png", "conf"},
			min: 1, max: 1, run: (*App).peerExport},
		{name: "peer rename", args: "<id> <new-name> | peer rename <id> --clear", flags: []string{"clear"},
			min: 1, max: 2, run: (*App).peerRename},
		{name: "peer set", args: "<id> --keepalive on|off", flags: []string{"keepalive"}, min: 1, max: 1, run: (*App).peerSet},
		{name: "peer disable", args: "<id>", min: 1, max: 1, run: func(a *App, ctx context.Context, c *call) error {
			return a.peerSetEnabled(ctx, c.pos[0], false)
		}},
		{name: "peer enable", args: "<id>", min: 1, max: 1, run: func(a *App, ctx context.Context, c *call) error {
			return a.peerSetEnabled(ctx, c.pos[0], true)
		}},
		{name: "peer rotate", args: "<id> --yes", flags: []string{"yes"}, min: 1, max: 1, run: (*App).peerRotate},
		{name: "peer rm", args: "<id> --yes", flags: []string{"yes"}, min: 1, max: 1, run: (*App).peerRemove},
	}},
	{"Migration (run with: docker run --rm --entrypoint awgctl -v <dir>:/config <image>)", []command{
		{name: "import-legacy", args: "<dir> [--dry-run] [--force-partial] [--rename OLD=NEW]...",
			flags: []string{"dry-run", "force-partial", "rename"}, min: 1, max: 1, run: (*App).cmdImportLegacy},
	}},
}

// usage is the help text. It comes from groups.
var usage = usageText()

func usageText() string {
	var b strings.Builder
	b.WriteString("Usage: awgctl <command> [arguments]\n\n")
	b.WriteString("Each change is saved in the database. Restart the container to apply it.\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "\n%s:\n", g.title)
		for _, c := range g.cmds {
			if c.help == "" {
				fmt.Fprintf(&b, "  %s\n", c.line())
				continue
			}
			fmt.Fprintf(&b, "  %-*s%s\n", max(38, len(c.line())+2), c.line(), c.help)
		}
	}
	b.WriteString("\nExit codes: 0 done, 1 error (nothing changed), 2 usage, 3 refused without --yes.\n")
	return b.String()
}

// findCommand returns the command of args and the arguments after its name.
func findCommand(args []string) (*command, []string, error) {
	top := args[0]
	var subs []*command
	for gi := range groups {
		for ci := range groups[gi].cmds {
			c := &groups[gi].cmds[ci]
			first, sub, hasSub := strings.Cut(c.name, " ")
			switch {
			case first != top:
			case !hasSub:
				return c, args[1:], nil
			case len(args) > 1 && args[1] == sub:
				return c, args[2:], nil
			default:
				subs = append(subs, c)
			}
		}
	}
	switch {
	case len(subs) == 0:
		return nil, nil, usagef("unknown command %q", top)
	case len(args) > 1 && !strings.HasPrefix(args[1], "-"):
		return nil, nil, usagef("unknown %s command %q", top, args[1])
	case len(subs) == 1:
		return nil, nil, subs[0].usageErr()
	}
	var names []string
	for _, c := range subs {
		names = append(names, strings.TrimPrefix(c.name, top+" "))
	}
	return nil, nil, usagef("usage: awgctl %s %s", top, strings.Join(names, "|"))
}
