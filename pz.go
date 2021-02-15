package main

import (
	"encoding/json"
	"fmt"
	"github.com/docker/cli/cli"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/command/container"
	cliflags "github.com/docker/cli/cli/flags"
	"github.com/pkg/browser"
	goridgeRpc "github.com/spiral/goridge/v3/pkg/rpc"
	"gopkg.in/yaml.v3"
	"io/ioutil"
	"math/rand"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

func main() {
	if _, err := os.Stat("./.pz.yaml"); os.IsNotExist(err) {
		fmt.Printf("Missing \"./.pz.yaml\" file.\r\n")

		os.Exit(1)
	}

	pzConfig := readPzConfig()
	dockerImage := pzConfig.ProjectZer0.DockerImage
	dockerEntrypoint := pzConfig.ProjectZer0.DockerEntrypoint
	if len(dockerImage) <= 0 {
		dockerImage = "projectzer0/pz-launcher"
	}
	if len(dockerEntrypoint) <= 0 {
		dockerEntrypoint = "/project/vendor/project-zer0/pz/docker/docker-entrypoint.sh"
	}

	wslPath := "NULL"
	if _, err := exec.LookPath("wslpath"); err == nil {
		cmd := exec.Command("wslpath", "-aw", "./")
		if result, err := cmd.Output(); err == nil {
			wslPath = strings.TrimRight(string(result), "\n")
		}
	}

	done := make(chan struct{})

	var ipcPort = randomTCPPort()

	go listenIPCServer(ipcPort, done)

	dockerCli, err := command.NewDockerCli()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		done <- struct{}{}

		os.Exit(1)
	}

	if err := runDocker(dockerCli, dockerImage, dockerEntrypoint, ipcPort, wslPath); err != nil {
		done <- struct{}{}
		if sterr, ok := err.(cli.StatusError); ok {
			if sterr.Status != "" {
				fmt.Fprintln(dockerCli.Err(), sterr.Status)
			}
			// StatusError should only be used for errors, and all errors should
			// have a non-zero exit status, so never exit with 0
			if sterr.StatusCode == 0 {
				os.Exit(1)
			}
			os.Exit(sterr.StatusCode)
		}
		fmt.Fprintln(dockerCli.Err(), err)

		os.Exit(1)
	}

	done <- struct{}{}
}

func runDocker(dockerCli *command.DockerCli, dockerImage string, dockerEntrypoint string, ipcPort int, wslPath string) error {
	currentDir, err := os.Getwd()
	if err != nil {
		return err
	}

	cmd := container.NewRunCommand(dockerCli)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	var opts = cliflags.NewClientOptions()
	opts.Common.SetDefaultOptions(cmd.Flags())

	if err := dockerCli.Initialize(opts); err != nil {
		return err
	}

	var defaultArgs = []string{
		"-e",
		"PZ_PWD=" + currentDir,
		"-e",
		"PZ_WSL_PATH=" + wslPath,
		"-e",
		fmt.Sprintf("PZ_PORT=%d", ipcPort),
		"-e",
		"PZ_LAUNCHER_VERSION=" + version,
		"--entrypoint=" + dockerEntrypoint,
		"-v",
		currentDir + ":/project",
		"-v",
		currentDir + "/.pz/.docker:/root/.docker",
		"-v",
		"/var/run/docker.sock:/var/run/docker.sock",
		"-w",
		"/project",
		"--rm",
		"-it",
		dockerImage,
	}

	cmd.SetArgs(append(defaultArgs, os.Args[1:]...))

	return cmd.Execute()
}

type PzApp struct{}

func (s *PzApp) OpenURL(payload string, r *string) error {
	type Json struct {
		Url string `json:"url"`
	}

	var packet Json
	if err := json.Unmarshal([]byte(payload), &packet); err != nil {
		panic(err)
	}

	if err := browser.OpenURL(packet.Url); err != nil {
		panic(err)
	}

	*r = "{\"status\":\"ok\"}"

	return nil
}

const (
	minTCPPort         = 0
	maxTCPPort         = 65535
	maxReservedTCPPort = 1024
	maxRandTCPPort     = maxTCPPort - (maxReservedTCPPort + 1)
)

var (
	tcpPortRand = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// isTCPPortAvailable returns a flag indicating whether or not a TCP port is
// available.
func isTCPPortAvailable(port int) bool {
	if port < minTCPPort || port > maxTCPPort {
		return false
	}
	conn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// randomTCPPort gets a free, random TCP port between 1025-65535. If no free
// ports are available -1 is returned.
func randomTCPPort() int {
	for i := maxReservedTCPPort; i < maxTCPPort; i++ {
		p := tcpPortRand.Intn(maxRandTCPPort) + maxReservedTCPPort + 1
		if isTCPPortAvailable(p) {
			return p
		}
	}
	return -1
}

func listenIPCServer(port int, done chan struct{}) {
	addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		panic(err)
	}

	server, err := net.ListenTCP("tcp", addr)
	if err != nil {
		panic(err)
	}

	defer server.Close()

	_ = rpc.Register(new(PzApp))

	for {
		select {
		case <-done:
			return
		default:
			if err := server.SetDeadline(time.Now().Add(time.Second)); err != nil {
				panic(err)
			}

			conn, err := server.Accept()
			if err != nil {
				if os.IsTimeout(err) {
					continue
				}

				panic(err)
			}

			go rpc.ServeCodec(goridgeRpc.NewCodec(conn))
		}
	}
}

type pzConfig struct {
	ProjectZer0 struct {
		DockerImage      string `yaml:"launcher_docker_image"`
		DockerEntrypoint string `yaml:"launcher_docker_entrypoint"`
	} `yaml:"project_zer0"`
}

func readPzConfig() pzConfig {
	yamlFile, err := ioutil.ReadFile("./.pz.yaml")
	if err != nil {
		panic(err)
	}

	var pzConfig pzConfig
	err = yaml.Unmarshal(yamlFile, &pzConfig)
	if err != nil {
		panic(err)
	}

	return pzConfig
}
