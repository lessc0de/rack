package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/urfave/cli.v1"

	"github.com/convox/rack/client"
	"github.com/convox/rack/cmd/convox/stdcli"
	"github.com/docker/docker/builder/dockerignore"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/fileutils"
)

var (
	buildCreateFlags = []cli.Flag{
		appFlag,
		rackFlag,
		cli.BoolFlag{
			Name:  "no-cache",
			Usage: "pull fresh image dependencies",
		},
		cli.BoolFlag{
			Name:  "incremental",
			Usage: "use incremental build",
		},
		cli.StringFlag{
			Name:  "file, f",
			Value: "docker-compose.yml",
			Usage: "path to an alternate docker compose manifest file",
		},
		cli.StringFlag{
			Name:  "description",
			Value: "",
			Usage: "description of the build",
		},
	}
)

func init() {
	stdcli.RegisterCommand(cli.Command{
		Name:        "build",
		Description: "create a new build",
		Usage:       "",
		Action:      cmdBuildsCreate,
		Flags:       buildCreateFlags,
	})
	stdcli.RegisterCommand(cli.Command{
		Name:        "builds",
		Description: "manage an app's builds",
		Usage:       "",
		Action:      cmdBuilds,
		Flags:       []cli.Flag{appFlag, rackFlag},
		Subcommands: []cli.Command{
			{
				Name:        "create",
				Description: "create a new build",
				Usage:       "",
				Action:      cmdBuildsCreate,
				Flags:       buildCreateFlags,
			},
			{
				Name:        "copy",
				Description: "copy a build to an app",
				Usage:       "<ID> <app>",
				Action:      cmdBuildsCopy,
				Flags: []cli.Flag{
					appFlag,
					rackFlag,
					cli.BoolFlag{
						Name:  "promote",
						Usage: "promote the release after copy",
					},
				},
			},
			{
				Name:        "info",
				Description: "print output for a build",
				Usage:       "<ID>",
				Action:      cmdBuildsInfo,
				Flags:       []cli.Flag{appFlag, rackFlag},
			},
			{
				Name:        "delete",
				Description: "Archive a build and its artifacts",
				Usage:       "<ID>",
				Action:      cmdBuildsDelete,
				Flags:       []cli.Flag{appFlag, rackFlag},
			},
		},
	})
}

func cmdBuilds(c *cli.Context) error {
	_, app, err := stdcli.DirApp(c, ".")
	if err != nil {
		return stdcli.ExitError(err)
	}

	if len(c.Args()) > 0 {
		return stdcli.ExitError(fmt.Errorf("`convox builds` does not take arguments. Perhaps you meant `convox builds create`?"))
	}

	if c.Bool("help") {
		stdcli.Usage(c, "")
		return nil
	}

	builds, err := rackClient(c).GetBuilds(app)
	if err != nil {
		return stdcli.ExitError(err)
	}

	t := stdcli.NewTable("ID", "STATUS", "RELEASE", "STARTED", "ELAPSED", "DESC")

	for _, build := range builds {
		started := humanizeTime(build.Started)
		elapsed := stdcli.Duration(build.Started, build.Ended)

		if build.Ended.IsZero() {
			elapsed = ""
		}

		t.AddRow(build.Id, build.Status, build.Release, started, elapsed, build.Description)
	}

	t.Print()
	return nil
}

func cmdBuildsCreate(c *cli.Context) error {
	wd := "."

	if len(c.Args()) > 0 {
		wd = c.Args()[0]
	}

	dir, app, err := stdcli.DirApp(c, wd)
	if err != nil {
		return stdcli.ExitError(err)
	}

	a, err := rackClient(c).GetApp(app)
	if err != nil {
		return stdcli.ExitError(err)
	}

	switch a.Status {
	case "creating":
		return stdcli.ExitError(fmt.Errorf("app is still creating: %s", app))
	case "running", "updating":
	default:
		return stdcli.ExitError(fmt.Errorf("unable to build app: %s", app))
	}

	if len(c.Args()) > 0 {
		dir = c.Args()[0]
	}

	release, err := executeBuild(c, dir, app, c.String("file"), c.String("description"))
	if err != nil {
		return stdcli.ExitError(err)
	}

	fmt.Printf("Release: %s\n", release)
	return nil
}

func cmdBuildsDelete(c *cli.Context) error {
	_, app, err := stdcli.DirApp(c, ".")
	if err != nil {
		return stdcli.ExitError(err)
	}

	if len(c.Args()) != 1 {
		stdcli.Usage(c, "delete")
		return nil
	}

	build := c.Args()[0]

	b, err := rackClient(c).DeleteBuild(app, build)
	if err != nil {
		return stdcli.ExitError(err)
	}

	fmt.Printf("Deleted %s\n", b.Id)
	return nil
}

func cmdBuildsInfo(c *cli.Context) error {
	_, app, err := stdcli.DirApp(c, ".")
	if err != nil {
		return stdcli.ExitError(err)
	}

	if len(c.Args()) != 1 {
		stdcli.Usage(c, "info")
		return nil
	}

	build := c.Args()[0]

	b, err := rackClient(c).GetBuild(app, build)
	if err != nil {
		return stdcli.ExitError(err)
	}

	fmt.Println(b.Logs)
	return nil
}

func cmdBuildsCopy(c *cli.Context) error {
	_, app, err := stdcli.DirApp(c, ".")
	if err != nil {
		return stdcli.ExitError(err)
	}

	if len(c.Args()) != 2 {
		stdcli.Usage(c, "copy")
		return nil
	}

	build := c.Args()[0]
	destApp := c.Args()[1]

	fmt.Print("Copying build... ")

	b, err := rackClient(c).CopyBuild(app, build, destApp)
	if err != nil {
		return stdcli.ExitError(err)
	}

	fmt.Println("OK")

	releaseID, err := finishBuild(c, destApp, b)
	if err != nil {
		return stdcli.ExitError(err)
	}

	if releaseID != "" {
		if c.Bool("promote") {
			fmt.Printf("Promoting %s %s... ", destApp, releaseID)

			_, err = rackClient(c).PromoteRelease(destApp, releaseID)
			if err != nil {
				return stdcli.ExitError(err)
			}

			fmt.Println("OK")
		} else {
			fmt.Printf("To deploy this copy run `convox releases promote %s --app %s`\n", releaseID, destApp)
		}
	}

	return nil
}

func executeBuild(c *cli.Context, source, app, manifest, description string) (string, error) {
	u, _ := url.Parse(source)

	switch u.Scheme {
	case "http", "https":
		return executeBuildUrl(c, source, app, manifest, description)
	default:
		if c.Bool("incremental") {
			return executeBuildDirIncremental(c, source, app, manifest, description)
		} else {
			return executeBuildDir(c, source, app, manifest, description)
		}
	}

	return "", fmt.Errorf("unreachable")
}

func createIndex(dir string) (client.Index, error) {
	index := client.Index{}

	err := warnUnignoredEnv(dir)
	if err != nil {
		return nil, err
	}

	ignore, err := readDockerIgnore(dir)
	if err != nil {
		return nil, err
	}

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}

	err = filepath.Walk(resolved, indexWalker(resolved, index, ignore))
	if err != nil {
		return nil, err
	}

	return index, nil
}

func indexWalker(root string, index client.Index, ignore []string) filepath.WalkFunc {
	return func(path string, info os.FileInfo, err error) error {
		rel, err := filepath.Rel(root, path)

		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		match, err := fileutils.Matches(rel, ignore)
		if err != nil {
			return err
		}

		if match {
			return nil
		}

		data, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}

		sum := sha256.Sum256(data)
		hash := hex.EncodeToString([]byte(sum[:]))

		index[hash] = client.IndexItem{
			Name:    rel,
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
			Size:    len(data),
		}

		return nil
	}
}

func readDockerIgnore(dir string) ([]string, error) {
	fd, err := os.Open(filepath.Join(dir, ".dockerignore"))

	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}

	ignore, err := dockerignore.ReadAll(fd)
	if err != nil {
		return nil, err
	}

	return ignore, nil
}

func uploadIndex(c *cli.Context, index client.Index) error {
	missing, err := rackClient(c).IndexMissing(index)
	if err != nil {
		return err
	}

	fmt.Print("Identifying changes... ")

	if len(missing) == 0 {
		fmt.Println("NONE")
		return nil
	}

	fmt.Printf("%d files\n", len(missing))

	buf := &bytes.Buffer{}

	gz := gzip.NewWriter(buf)

	tw := tar.NewWriter(gz)

	for _, m := range missing {
		data, err := ioutil.ReadFile(index[m].Name)
		if err != nil {
			return err
		}

		header := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     m,
			Mode:     0600,
			Size:     int64(len(data)),
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if _, err := tw.Write(data); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}

	if err := gz.Close(); err != nil {
		return err
	}

	progress := func(s string) {
		fmt.Printf("\rUploading... %s       ", strings.TrimSpace(s))
	}

	if err := rackClient(c).IndexUpdate(buf.Bytes(), progress); err != nil {
		return err
	}

	fmt.Println()

	return nil
}

func executeBuildDirIncremental(c *cli.Context, dir, app, manifest, description string) (string, error) {
	system, err := rackClient(c).GetSystem()
	if err != nil {
		return "", err
	}

	// if the rack doesnt support incremental builds then fall back
	if system.Version < "20160226234213" {
		return executeBuildDir(c, dir, app, manifest, description)
	}

	cache := !c.Bool("no-cache")

	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	fmt.Printf("Analyzing source... ")

	index, err := createIndex(dir)
	if err != nil {
		return "", err
	}

	fmt.Println("OK")

	err = uploadIndex(c, index)
	if err != nil {
		return "", err
	}

	fmt.Printf("Starting build... ")

	build, err := rackClient(c).CreateBuildIndex(app, index, cache, manifest, description)
	if err != nil {
		return "", err
	}

	fmt.Println("OK")

	return finishBuild(c, app, build)
}

func executeBuildDir(c *cli.Context, dir, app, manifest, description string) (string, error) {
	err := warnUnignoredEnv(dir)
	if err != nil {
		return "", err
	}

	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	fmt.Print("Creating tarball... ")

	tar, err := createTarball(dir)
	if err != nil {
		return "", err
	}

	fmt.Println("OK")

	cache := !c.Bool("no-cache")

	build, err := rackClient(c).CreateBuildSourceProgress(app, tar, cache, manifest, description, func(s string) {
		// Pad string with spaces at the end to clear any text left over from a longer string.
		fmt.Printf("\rUploading... %s       ", strings.TrimSpace(s))
	})
	if err != nil {
		return "", err
	}

	fmt.Println()

	return finishBuild(c, app, build)
}

func executeBuildUrl(c *cli.Context, url, app, manifest, description string) (string, error) {
	cache := !c.Bool("no-cache")

	build, err := rackClient(c).CreateBuildUrl(app, url, cache, manifest, description)
	if err != nil {
		return "", err
	}

	return finishBuild(c, app, build)
}

func createTarball(base string) ([]byte, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	sym, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}

	err = os.Chdir(sym)
	if err != nil {
		return nil, err
	}

	var includes = []string{"."}
	var excludes []string

	dockerIgnorePath := path.Join(sym, ".dockerignore")
	dockerIgnore, err := os.Open(dockerIgnorePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		//There is no docker ignore
		excludes = make([]string, 0)
	} else {
		excludes, err = dockerignore.ReadAll(dockerIgnore)
		if err != nil {
			return nil, err
		}
	}

	// If .dockerignore mentions .dockerignore or the Dockerfile
	// then make sure we send both files over to the daemon
	// because Dockerfile is, obviously, needed no matter what, and
	// .dockerignore is needed to know if either one needs to be
	// removed.  The deamon will remove them for us, if needed, after it
	// parses the Dockerfile.
	keepThem1, _ := fileutils.Matches(".dockerignore", excludes)
	keepThem2, _ := fileutils.Matches("Dockerfile", excludes)
	if keepThem1 || keepThem2 {
		includes = append(includes, ".dockerignore", "Dockerfile")
	}

	// if err := builder.ValidateContextDirectory(contextDirectory, excludes); err != nil {
	// 	return nil, fmt.Errorf("Error checking context is accessible: '%s'. Please check permissions and try again.", err)
	// }

	options := &archive.TarOptions{
		Compression:     archive.Gzip,
		ExcludePatterns: excludes,
		IncludeFiles:    includes,
	}

	out, err := archive.TarWithOptions(sym, options)
	if err != nil {
		return nil, err
	}

	bytes, err := ioutil.ReadAll(out)
	if err != nil {
		return nil, err
	}

	err = os.Chdir(cwd)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

func finishBuild(c *cli.Context, app string, build *client.Build) (string, error) {
	if build.Id == "" {
		return "", fmt.Errorf("unable to fetch build id")
	}

	reader, writer := io.Pipe()
	go io.Copy(os.Stdout, reader)

	err := rackClient(c).StreamBuildLogs(app, build.Id, writer)
	if err != nil {
		return "", err
	}

	release, err := waitForBuild(c, app, build.Id)
	if err != nil {
		return "", err
	}

	return release, nil
}

func waitForBuild(c *cli.Context, app, id string) (string, error) {

	for {
		build, err := rackClient(c).GetBuild(app, id)
		if err != nil {
			return "", err
		}

		switch build.Status {
		case "complete":
			return build.Release, nil
		case "error":
			return "", fmt.Errorf("%s build failed", app)
		case "failed":
			return "", fmt.Errorf("%s build failed", app)
		case "timeout":
			return "", fmt.Errorf("%s build timed out", app)
		}

		time.Sleep(1 * time.Second)
	}

	return "", fmt.Errorf("can't get here")
}

func warnUnignoredEnv(dir string) error {
	hasDockerIgnore := false
	hasDotEnv := false
	warn := false

	if _, err := os.Stat(".env"); err == nil {
		hasDotEnv = true
	}

	if _, err := os.Stat(".dockerignore"); err == nil {
		hasDockerIgnore = true
	}

	if !hasDockerIgnore && hasDotEnv {
		warn = true
	} else if hasDockerIgnore && hasDotEnv {
		lines, err := readDockerIgnore(dir)
		if err != nil {
			return err
		}

		if len(lines) == 0 {
			warn = true
		} else {
			warn = true
			for _, line := range lines {
				if line == ".env" {
					warn = false
					break
				}
			}
		}
	}
	if warn {
		fmt.Println("WARNING: You have a .env file that is not in your .dockerignore, you may be leaking secrets")
	}
	return nil
}
