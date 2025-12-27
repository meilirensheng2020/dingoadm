/*
 * Copyright (c) 2025 dingodb.com, Inc. All Rights Reserved
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package fs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dingodb/dingoadm/cli/cli"
	"github.com/dingodb/dingoadm/internal/utils"
	"github.com/spf13/cobra"
)

const (
	FS_MOUNT_EXAMPLE = `Examples:
   $ dingocli fs mount mds://10.220.69.6:7400/fs1 /mnt/dingofs
   $ dingocli fs mount local://fs1 /mnt/dingofs`
)

const (
	DINGOFS_CLIENT = "/home/yansp/.dingofs/bin/dingo-client"
)

type mountOptions struct {
	client     string
	cmdArgs    []string
	mountpoint string
	daemonize  bool
}

func NewFsMountCommand(dingoadm *cli.DingoAdm) *cobra.Command {
	var options mountOptions

	cmd := &cobra.Command{
		Use:                "mount METAURL MOUNTPOINT [OPTIONS]",
		Short:              "mount filesystem",
		Args:               utils.RequiresMinArgs(0),
		DisableFlagParsing: true,
		Example:            FS_MOUNT_EXAMPLE,
		RunE: func(cmd *cobra.Command, args []string) error {
			// check flags
			for _, arg := range args {
				if arg == "--help" || arg == "-h" {
					return runCommandHelp(cmd, DINGOFS_CLIENT)
				}
				if arg == "--daemonize" || arg == "-d" {
					options.daemonize = true
				}
			}

			if len(args) < 2 {
				return fmt.Errorf("\"dingoadm fs mount\" requires exactly 2 arguments\n\nUsage: dingoadm fs mount METAURL MOUNTPOINT [OPTIONS]")
			}

			options.client = DINGOFS_CLIENT
			options.cmdArgs = args
			options.mountpoint = args[1]

			return runMount(cmd, dingoadm, options)
		},
		SilenceUsage:          false,
		DisableFlagsInUseLine: true,
	}

	utils.SetFlagErrorFunc(cmd)

	return cmd
}

func runMount(cmd *cobra.Command, dingoadm *cli.DingoAdm, options mountOptions) error {
	var oscmd *exec.Cmd
	var name string

	name = options.client
	cmdarg := options.cmdArgs

	oscmd = exec.Command(name, cmdarg...)

	stdout, err := oscmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := oscmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := oscmd.Start(); err != nil {
		return err
	}

	// forground mode, wait process exit
	if !options.daemonize {
		// realtime output to console
		go io.Copy(os.Stdout, stdout)
		go io.Copy(os.Stderr, stderr)

		// wait process complete
		if err := oscmd.Wait(); err != nil {
			return err
		}

		return nil
	}

	// daemonize mode
	var wg sync.WaitGroup
	wg.Add(3)

	daemonReady := make(chan bool, 1)
	daemonFailed := make(chan error, 1)

	// read stdout
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Println(line)

			if strings.Contains(strings.ToLower(line), "ready") {
				select {
				case daemonReady <- true:
				default:
				}
			}
		}

		if err := scanner.Err(); err != nil {
			fmt.Printf("read stdout error: %v\n", err)
		}
	}()

	// read stderr
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(os.Stderr, line)

			lowerLine := strings.ToLower(line)
			if strings.Contains(lowerLine, "error") {
				select {
				case daemonFailed <- fmt.Errorf("start dingo-client failed: %s", line):
				default:
				}
			}
		}

		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "read stderr error: %v\n", err)
		}
	}()

	// mount completed
	go func() {
		filename := filepath.Join(options.mountpoint, ".stats")
		defer wg.Done()

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			if _, err := os.Stat(filename); !os.IsNotExist(err) {
				select {
				case daemonReady <- true:
				default:
					continue
				}
				return
			}
		}
	}()

	select {
	case <-daemonReady: // start success
		// continues to read the remaining output
		go func() {
			wg.Wait()
			// wait daemon exit, non block
			go oscmd.Wait()
		}()

		fmt.Printf("Successfully mounted %s (PID: %d)\n", options.mountpoint, oscmd.Process.Pid)
		return nil

	case err := <-daemonFailed: //start failed
		fmt.Println("Daemon startup failed, killing process...")
		if killErr := oscmd.Process.Kill(); killErr != nil {
			return killErr
		}
		wg.Wait()
		oscmd.Wait()
		return err
	}
}

func runCommandHelp(cmd *cobra.Command, command string) error {
	// print dingocli usage
	fmt.Printf("Usage: dingocli fs %s\n", cmd.Use)
	fmt.Println("")
	fmt.Println(cmd.Short)
	fmt.Println("")

	// print  dingo-client options
	fmt.Println("Options:")

	helpArgs := []string{"--help"}
	oscmd := exec.Command(command, helpArgs...)
	output, err := oscmd.CombinedOutput()
	if err != nil && len(output) == 0 {
		return err
	}

	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			fmt.Printf("  %s\n", trimmed)
		}
	}

	// print dingocli example
	fmt.Println("")
	fmt.Println(cmd.Example)

	return nil
}
