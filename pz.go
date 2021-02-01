package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/pkg/browser"
	goridgeRpc "github.com/spiral/goridge/v3/pkg/rpc"
	"golang.org/x/crypto/ssh/terminal"
	"gopkg.in/yaml.v3"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/rpc"
	"os"
	"strings"
	"time"
)

func main() {
	if _, err := os.Stat("./.pz.yaml"); os.IsNotExist(err) {
		fmt.Printf("Missing \"./.pz.yaml\" file.\r\n")

		os.Exit(1)
	}

	pzConfig := ReadPzConfig()
	dockerImage := pzConfig.ProjectZer0.DockerImage
	if len(dockerImage) <= 0 {
		dockerImage = "projectzer0/pz-launcher"
	}

	ctx := context.Background()
	done := make(chan struct{})

	go listenIPCServer(done)

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	cont, err := ContainerCreate(cli, ctx, dockerImage)

	waiter, err := cli.ContainerAttach(ctx, cont.ID, types.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})

	if err != nil {
		panic(err)
	}

	go io.Copy(os.Stdout, waiter.Reader)
	go io.Copy(os.Stderr, waiter.Reader)

	if err := cli.ContainerStart(ctx, cont.ID, types.ContainerStartOptions{}); err != nil {
		panic(err)
	}

	fd := int(os.Stdin.Fd())
	var oldState *terminal.State
	if terminal.IsTerminal(fd) {
		oldState, err = terminal.MakeRaw(fd)
		if err != nil {
			panic(err)
		}

		go func() {
			for {
				consoleReader := bufio.NewReaderSize(os.Stdin, 1)
				input, _ := consoleReader.ReadByte()

				// Ctrl-C = 3
				if input == 3 {
					if err := cli.ContainerRemove(ctx, cont.ID, types.ContainerRemoveOptions{
						Force: true,
					}); err != nil {
						panic(err)
					}

				}

				waiter.Conn.Write([]byte{input})
			}
		}()
	}

	statusCh, errCh := cli.ContainerWait(ctx, cont.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			panic(err)
		}
	case <-statusCh:
	}

	if terminal.IsTerminal(fd) {
		terminal.Restore(fd, oldState)
	}

	done <- struct{}{}
}

type PzApp struct {}
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

func listenIPCServer(done chan struct{}) {
	addr, err := net.ResolveTCPAddr("tcp", ":45666")
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

func ContainerCreate(cli *client.Client, ctx context.Context, dockerImage string) (container.ContainerCreateCreatedBody, error) {
	dir, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		OpenStdin:    true,
		Env: []string{
			"PZ_PWD=" + dir,
		},
		Cmd:        os.Args[1:],
		Image:      dockerImage,
		WorkingDir: "/project",
		Entrypoint: []string{"/project/vendor/project-zer0/pz/docker/docker-entrypoint.sh"},
	}, &container.HostConfig{
		AutoRemove: true,
		Binds: []string{
			dir + ":/project",
			dir + "/.pz/.docker:/root/.docker",
			"/var/run/docker.sock:/var/run/docker.sock",
		},
	}, nil, nil, "")

	if err != nil {
		if !strings.Contains(err.Error(), " No such image") {
			fmt.Println("Error creating project-zer0/pz container")

			panic(err)
		}

		PullImage(cli, ctx, dockerImage)

		return ContainerCreate(cli, ctx, dockerImage)
	}

	return resp, err
}

func PullImage(cli *client.Client, ctx context.Context, dockerImage string) {
	fmt.Println("Pulling \"" + dockerImage + "\" image from registry")

	reader, err := cli.ImagePull(
		ctx,
		dockerImage,
		types.ImagePullOptions{},
	)

	if err != nil {
		fmt.Println("Error \"" + dockerImage + "\" image from registry")
		panic(err)
	}

	io.Copy(os.Stdout, reader)
}

type PzConfig struct {
	ProjectZer0 struct {
		DockerImage string `yaml:"launcher_docker_image"`
	} `yaml:"project_zer0"`
}


func ReadPzConfig() PzConfig {
	yamlFile, err := ioutil.ReadFile("./.pz.yaml")
	if err != nil {
		panic(err)
	}

	var pzConfig PzConfig
	err = yaml.Unmarshal(yamlFile, &pzConfig)
	if err != nil {
		panic(err)
	}

	return pzConfig
}