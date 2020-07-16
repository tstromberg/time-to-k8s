package main

import (
	"bytes"
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

var iterationCount = flag.Int("iterations", 20, "How many runs to execute")

// ExperimentResult stores the result of a single experiment run
type ExperimentResult struct {
	Name       string
	Args       []string
	Version    string
	Startup    time.Duration
	Running    time.Duration
	Deployment time.Duration
	Execution  time.Duration
	Total      time.Duration
	Timestamp  time.Time
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
	klog.V(1).Infof("Running: %s", cmd)
	err := cmd.Run()
	rr.Duration = time.Since(start)

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			rr.ExitCode = exitError.ExitCode()
		}
	}

	klog.V(1).Infof("Completed: %s (duration: %s, exit code: %d, err: %v)", cmd, rr.Duration, rr.ExitCode, err)
	if len(rr.Stderr.Bytes()) > 0 {
		klog.V(1).Infof("stderr:\n%s\n", rr.Stderr.String())
	}

	return rr, err
}

// RetryRun retries until a command succeeds, returning the full duration
func RetryRun(cmd *exec.Cmd) (*RunResult, error) {
	var rr *RunResult
	var err error
	klog.Infof("Running %s until it succeeds ...", cmd)

	duration := time.Duration(0)
	attempts := 0

	for attempts < 1000 {
		// exec.Cmd can only be executed once, so build a new one)
		rr, err = Run(exec.Command(cmd.Path, cmd.Args[1:]...))
		duration += rr.Duration
		rr.Duration = duration

		if err == nil {
			klog.V(1).Infof("%s succeeded after %d attempts (duration: %s)", cmd.Args, attempts, duration)
			return rr, err
		}

		attempts++
		klog.V(1).Infof("%s failed: %v (%d attempts)", cmd, err, attempts)
		// brief break to avoid DoS attack
		time.Sleep(10 * time.Millisecond)
	}

	return rr, err
}

func ds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}

func runIteration(name string, setupCmd string, cleanupCmd string) (ExperimentResult, error) {
	setup := strings.Split(setupCmd, " ")
	cleanup := strings.Split(cleanupCmd, " ")
	binary := setup[0]

	klog.Infof("starting %q iteration. initialization args: %v, cleanup args: %v", name, setup, cleanup)

	e := ExperimentResult{Name: name, Timestamp: time.Now()}

	rr, err := Run(exec.Command(binary, "version"))
	if err != nil {
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.Version = strings.Split(rr.Stdout.String(), "\n")[0]

	rr, err = Run(exec.Command(binary, setup[1:]...))
	if err != nil {
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.Startup = rr.Duration

	extraArgs := []string{}

	// k3d does not automatically configure kubectl :(
	if strings.Contains(binary, "k3d") {
		rr, err = RetryRun(exec.Command(binary, "get-kubeconfig"))
		e.Deployment += rr.Duration
		if err != nil {
			return e, fmt.Errorf("%s failed: %w", rr, err)
		}
		extraArgs = []string{"--kubeconfig", strings.TrimSpace(rr.Stdout.String())}
	}

	if strings.Contains(binary, "kind") {
		extraArgs = []string{"--context", "kind-kind"}
	}
	if strings.Contains(binary, "minikube") {
		extraArgs = []string{"--context", "minikube"}
	}

	args := append(extraArgs, "get", "po", "-A")
	for {
		rr, err = RetryRun(exec.Command("kubectl", args...))
		if err != nil {
			return e, fmt.Errorf("%s failed: %w", rr, err)
		}
		e.Running += rr.Duration

		// During startup, the apiserver returns an empty list. Don't consider it valid until a kube-system pod is seen.
		if strings.Contains(rr.Stdout.String(), "kube-system") {
			break
		}
	}

	args = append(extraArgs, "apply", "-f", "deployment.yaml")
	rr, err = RetryRun(exec.Command("kubectl", args...))
	if err != nil {
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.Deployment += rr.Duration

	args = append(extraArgs, "exec", "deployment/netcat", "--", "nc", "-v", "localhost", "8080")
	rr, err = RetryRun(exec.Command("kubectl", args...))
	if err != nil {
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.Execution = rr.Duration
	e.Total = e.Startup + e.Running + e.Deployment + e.Execution

	rr, err = RetryRun(exec.Command(cleanup[0], cleanup[1:]...))
	if err != nil {
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}

	return e, nil
}

func main() {
	klog.InitFlags(nil)
	tf, err := ioutil.TempFile("", "time-to-k8s.*.csv")
	if err != nil {
		klog.Exitf("tempfile: %v", err)
	}

	c := csv.NewWriter(tf)

	c.Write([]string{"name", "args", "platform", "iteration", "time", "version", "startup (seconds)", "apiserver ready (seconds)", "deployment (seconds)", "deployment complete (seconds)", "total duration (seconds)"})
	klog.Infof("Writing output to %s", tf.Name())
	c.Flush()

	testCases := map[string][]string{
		"minikube":              []string{"minikube start", "minikube delete --all"},
		"k3d":                   []string{"c", "d"},
		"kind":                  []string{"create cluster", "delete cluster"},
		"minikube_refresh_70k":  []string{"minikube start --extra-config etcd.proxy-refresh-interval=70000", "minikube delete --all"},
		"minikube_refresh_700k": []string{"minikube start --extra-config etcd.proxy-refresh-interval=700000", "minikube delete --all"},
	}

	// quick cleanup loop
	for name, commands := range testCases {
		cleanup := strings.Split(commands[1], " ")
		klog.Infof("cleaning up %q with arguments: %v", name, cleanup)
		RetryRun(exec.Command(cleanup[0], cleanup[1:]...))
	}

	for i := 0; i <= *iterationCount; i++ {
		if i == 0 {
			klog.Infof("Starting dry-run iteration - will not record results")
		} else {
			klog.Infof("STARTING ITERATION COUNT %d of %d", i, *iterationCount)
		}

		for name, commands := range testCases {
			e, err := runIteration(name, commands[0], commands[1])
			if err != nil {
				klog.Exitf("%s experiment failed: %v", name, err)
			}
			klog.Infof("Result: %+v", e)
			if i == 0 {
				continue
			}
			klog.Infof("Updating %s ...", tf.Name())
			c.Write([]string{name, strings.Join(e.Args, " "), runtime.GOOS, fmt.Sprintf("%d", i), e.Timestamp.String(), e.Version, ds(e.Startup), ds(e.Running), ds(e.Deployment), ds(e.Execution), ds(e.Total)})
			c.Flush()
		}
	}

}
