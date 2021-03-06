package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/convox/rack/cmd/convox/stdcli"
	"github.com/convox/version"
	"gopkg.in/urfave/cli.v1"
)

func init() {
	stdcli.RegisterCommand(cli.Command{
		Name:        "rack",
		Description: "manage your Convox rack",
		Usage:       "",
		Action:      cmdRack,
		Flags:       []cli.Flag{rackFlag},
		Subcommands: []cli.Command{
			{
				Name:        "logs",
				Description: "stream the rack logs",
				Usage:       "",
				Action:      cmdRackLogs,
				Flags: []cli.Flag{
					rackFlag,
					cli.StringFlag{
						Name:  "filter",
						Usage: "filter the logs by a given token",
					},
					cli.BoolTFlag{
						Name:  "follow",
						Usage: "keep streaming new log output (default)",
					},
					cli.DurationFlag{
						Name:  "since",
						Usage: "show logs since a duration (e.g. 10m or 1h2m10s)",
						Value: 2 * time.Minute,
					},
				},
			},
			{
				Name:        "params",
				Description: "list advanced rack parameters",
				Usage:       "",
				Action:      cmdRackParams,
				Flags:       []cli.Flag{rackFlag},
				Subcommands: []cli.Command{
					{
						Name:        "set",
						Description: "update advanced rack parameters",
						Usage:       "NAME=VALUE [NAME=VALUE]",
						Action:      cmdRackParamsSet,
						Flags:       []cli.Flag{rackFlag},
					},
				},
			},
			{
				Name:        "ps",
				Description: "list rack processes",
				Usage:       "",
				Action:      cmdRackPs,
				Flags: []cli.Flag{
					rackFlag,
					cli.BoolFlag{
						Name:  "stats",
						Usage: "display process cpu/memory stats",
					},
				},
			},
			{
				Name:        "scale",
				Description: "scale the rack capacity",
				Usage:       "",
				Action:      cmdRackScale,
				Flags: []cli.Flag{
					rackFlag,
					cli.IntFlag{
						Name:  "count",
						Usage: "horizontally scale the instance count, e.g. 3 or 10",
					},
					cli.StringFlag{
						Name:  "type",
						Usage: "vertically scale the instance type, e.g. t2.small or c3.xlarge",
					},
				},
			},
			{
				Name:        "update",
				Description: "update rack to the given version",
				Usage:       "[version]",
				Action:      cmdRackUpdate,
				Flags:       []cli.Flag{rackFlag},
			},
			{
				Name:        "releases",
				Description: "list rack releases",
				Usage:       "",
				Action:      cmdRackReleases,
				Flags: []cli.Flag{
					rackFlag,
					cli.BoolFlag{
						Name:  "unpublished",
						Usage: "include unpublished versions",
					},
				},
			},
		},
	})
}

func cmdRack(c *cli.Context) error {
	if len(c.Args()) > 0 {
		return stdcli.ExitError(fmt.Errorf("`convox rack` does not take arguments. Perhaps you meant `convox rack update`?"))
	}

	if c.Bool("help") {
		stdcli.Usage(c, "")
		return nil
	}

	system, err := rackClient(c).GetSystem()
	if err != nil {
		return stdcli.ExitError(err)
	}

	fmt.Printf("Name     %s\n", system.Name)
	fmt.Printf("Status   %s\n", system.Status)
	fmt.Printf("Version  %s\n", system.Version)
	fmt.Printf("Region   %s\n", system.Region)
	fmt.Printf("Count    %d\n", system.Count)
	fmt.Printf("Type     %s\n", system.Type)
	return nil
}

func cmdRackLogs(c *cli.Context) error {
	err := rackClient(c).StreamRackLogs(c.String("filter"), c.BoolT("follow"), c.Duration("since"), os.Stdout)
	if err != nil {
		return stdcli.ExitError(err)
	}

	return nil
}

func cmdRackParams(c *cli.Context) error {
	system, err := rackClient(c).GetSystem()
	if err != nil {
		return stdcli.ExitError(err)
	}

	params, err := rackClient(c).ListParameters(system.Name)
	if err != nil {
		return stdcli.ExitError(err)
	}

	keys := []string{}

	for key, _ := range params {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	t := stdcli.NewTable("NAME", "VALUE")

	for _, key := range keys {
		t.AddRow(key, params[key])
	}

	t.Print()
	return nil
}

func cmdRackParamsSet(c *cli.Context) error {
	system, err := rackClient(c).GetSystem()
	if err != nil {
		return stdcli.ExitError(err)
	}

	params := map[string]string{}

	for _, arg := range c.Args() {
		parts := strings.SplitN(arg, "=", 2)

		if len(parts) != 2 {
			return stdcli.ExitError(fmt.Errorf("invalid argument: %s", arg))
		}

		params[parts[0]] = parts[1]
	}

	fmt.Print("Updating parameters... ")

	err = rackClient(c).SetParameters(system.Name, params)
	if err != nil {
		return stdcli.ExitError(err)
	}

	fmt.Println("OK")
	return nil
}

func cmdRackPs(c *cli.Context) error {
	system, err := rackClient(c).GetSystem()
	if err != nil {
		return stdcli.ExitError(err)
	}

	ps, err := rackClient(c).GetProcesses(system.Name, c.Bool("stats"))
	if err != nil {
		return stdcli.ExitError(err)
	}

	if c.Bool("stats") {
		fm, err := rackClient(c).ListFormation(system.Name)
		if err != nil {
			return stdcli.ExitError(err)
		}

		displayProcessesStats(ps, fm)
		return nil
	}

	displayProcesses(ps)

	return nil
}

func cmdRackUpdate(c *cli.Context) error {
	versions, err := version.All()
	if err != nil {
		return stdcli.ExitError(err)
	}

	specified := "stable"

	if len(c.Args()) > 0 {
		specified = c.Args()[0]
	}

	version, err := versions.Resolve(specified)
	if err != nil {
		return stdcli.ExitError(err)
	}

	system, err := rackClient(c).UpdateSystem(version.Version)
	if err != nil {
		return stdcli.ExitError(err)
	}

	fmt.Printf("Name     %s\n", system.Name)
	fmt.Printf("Status   %s\n", system.Status)
	fmt.Printf("Version  %s\n", system.Version)
	fmt.Printf("Count    %d\n", system.Count)
	fmt.Printf("Type     %s\n", system.Type)

	fmt.Println()
	fmt.Printf("Updating to version: %s\n", version.Version)
	return nil
}

func cmdRackScale(c *cli.Context) error {
	// initialize to invalid values that indicate no change
	count := -1
	typ := ""

	if c.IsSet("count") {
		count = c.Int("count")
	}

	if c.IsSet("type") {
		typ = c.String("type")
	}

	// validate no argument
	switch len(c.Args()) {
	case 0:
		if count == -1 && typ == "" {
			displaySystem(c)
			return nil
		}
		// fall through to scale API call
	default:
		stdcli.Usage(c, "scale")
		return nil
	}

	_, err := rackClient(c).ScaleSystem(count, typ)
	if err != nil {
		return stdcli.ExitError(err)
	}

	displaySystem(c)
	return nil
}

func cmdRackReleases(c *cli.Context) error {
	system, err := rackClient(c).GetSystem()
	if err != nil {
		return stdcli.ExitError(err)
	}

	pendingVersion := system.Version

	releases, err := rackClient(c).GetSystemReleases()
	if err != nil {
		return stdcli.ExitError(err)
	}

	t := stdcli.NewTable("VERSION", "UPDATED", "STATUS")

	for i, r := range releases {
		status := ""

		if system.Status == "updating" && i == 0 {
			pendingVersion = r.Id
			status = "updating"
		}

		if system.Version == r.Id {
			status = "active"
		}

		t.AddRow(r.Id, humanizeTime(r.Created), status)
	}

	t.Print()

	next, err := version.Next(system.Version)
	if err != nil {
		return stdcli.ExitError(err)
	}

	if next > pendingVersion {
		// if strings.Compare(next, pendingVersion) == 1 {
		fmt.Println()
		fmt.Printf("New version available: %s\n", next)
	}

	return nil
}

func displaySystem(c *cli.Context) {
	system, err := rackClient(c).GetSystem()
	if err != nil {
		stdcli.Error(err)
		return
	}

	fmt.Printf("Name     %s\n", system.Name)
	fmt.Printf("Status   %s\n", system.Status)
	fmt.Printf("Version  %s\n", system.Version)
	fmt.Printf("Count    %d\n", system.Count)
	fmt.Printf("Type     %s\n", system.Type)
}
