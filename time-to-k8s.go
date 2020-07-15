package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// ExperimentResult stores the result of a single experiment run
type ExperimentResult struct {
	Program    string
	Version    string
	Startup    time.Duration
	Ready      time.Duration
	Deployment time.Duration
	Execution  time.Duration
	Total      time.Duration
}

// RunResult stores the result of an cmd.Run call
type RunResult struct {
	Stdout   *bytes.Buffer
	Stderr   *bytes.Buffer
	ExitCode int
	Duration time.Duration
	Args     []string
}

// Run is a helper to log command execution
func Run(cmd *exec.Cmd) (*RunResult, error) {
	rr := &RunResult{Args: cmd.Args}

	var outb, errb bytes.Buffer
	cmd.Stdout, rr.Stdout = &outb, &outb
	cmd.Stderr, rr.Stderr = &errb, &errb

	start := time.Now()
	klog.Infof("Running: %s", cmd)
	err := cmd.Run()
	rr.Duration = time.Since(start)

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			rr.ExitCode = exitError.ExitCode()
		}
	}

	klog.Infof("Completed: %s (duration: %s, exit code: %d, err: %v)", cmd, rr.Duration, rr.ExitCode, err)
	if len(rr.Stderr.Bytes()) > 0 {
		klog.Errorf("stderr:\n%s\n", rr.Stderr.String())
	}

	return rr, err
}

// RetryRun retries until a command succeeds, returning the full duration
func RetryRun(cmd *exec.Cmd) (*RunResult, error) {
	var rr *RunResult
	var err error

	duration := time.Duration(0)
	attempts := 0

	for attempts < 1000 {
		// exec.Cmd can only be executed once, so build a new one)
		rr, err = Run(exec.Command(cmd.Path, cmd.Args[1:]...))
		duration += rr.Duration
		rr.Duration = duration

		if err == nil {
			klog.Infof("SUCCESS!")
			return rr, err
		}

		attempts++
		klog.Warningf("%s failed: %v (%d attempts)", cmd, err, attempts)
	}

	return rr, err
}

func ds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

func main() {
	klog.InitFlags(nil)
	c := csv.NewWriter(file.


	c.Write([]string{"program", "version", "startup duration (seconds)", "deployment duration (seconds)", "execution duration (seconds)", "total duration (seconds)"})

	testCases := map[string][]string{
		"k3d":      []string{"c", "d"},
		"kind":     []string{"create cluster", "delete cluster"},
		"minikube": []string{"start", "delete"},
	}

	// cleanup

	// cleanup

	for binary, commands := range testCases {
		setup := commands[0]
		cleanup := commands[1]
		e := ExperimentResult{Program: binary}

		Run(exec.Command(binary, cleanup))


	for binary, commands := range testCases {
		setup := commands[0]
		cleanup := commands[1]
		e := ExperimentResult{Program: binary}

		Run(exec.Command(binary, cleanup))

		rr, err := Run(exec.Command(binary, "version"))
		if err != nil {
			klog.Exitf("%s failed: %v", rr, err)
		}
		e.Version = strings.Split(rr.Stdout.String(), "\n")[0]

		rr, err = Run(exec.Command(binary, setup))
		if err != nil {
			klog.Exitf("%s failed: %v", rr, err)
		}
		e.Startup = rr.Duration

		extraArgs := []string{}

		// k3d does not automatically configure kubectl :(
		if strings.Contains(binary, "k3d") {
			rr, err = RetryRun(exec.Command(binary, "get-kubeconfig"))
			e.Deployment += rr.Duration
			if err != nil {
				klog.Exitf("%s failed: %v", rr, err)
			}
			extraArgs = []string{"--kubeconfig", rr.Stdout.String()}
		}

		args := append(extraArgs, "get", "po", "-A")
		rr, err = RetryRun(exec.Command("kubectl", args...))
		if err != nil {
			klog.Exitf("%s failed: %v", rr, err)
		}
		e.Deployment += rr.Duration


		args := append(extraArgs, "apply", "-f", "deployment.yaml")
		rr, err = RetryRun(exec.Command("kubectl", args...))
		if err != nil {
			klog.Exitf("%s failed: %v", rr, err)
		}
		e.Deployment += rr.Duration

		args = append(extraArgs, "exec", "deployment/netcat", "--", "nc", "-v", "localhost", "8080")
		rr, err = RetryRun(exec.Command("kubectl", args...))
		if err != nil {
			klog.Exitf("%s failed: %v", rr, err)
		}
		e.Execution = rr.Duration
		e.Total = e.Startup + e.Deployment + e.Execution

		fmt.Printf("experiment: %+v", e)
		c.Write([]string{binary, e.Version, ds(e.Startup), ds(e.Deployment), ds(e.Execution), ds(e.Total)})

		rr, err = Run(exec.Command(binary, cleanup))
		if err != nil {
			klog.Exitf("%s failed: %v", rr, err)
		}
	}
}
