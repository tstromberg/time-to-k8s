package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
	"k8s.io/klog/v2"
)

var iterationCount = flag.Int("iterations", 10, "How many runs to execute")
var configPath = flag.String("config", "", "configuration file to load test cases from")
var testTimeout = flag.Duration("timeout", 6*time.Minute, "maximum time a test can take")

// ExperimentResult stores the result of a single experiment run
type ExperimentResult struct {
	Name          string
	Args          []string
	Version       string
	Startup       time.Duration
	APIAnswering  time.Duration
	KubernetesSvc time.Duration
	DNSSvc        time.Duration
	AppRunning    time.Duration
	DNSAnswering  time.Duration
	Total         time.Duration
	ExitCode      int
	Error         string
	Timestamp     time.Time
}

// RunResult stores the result of an cmd.Run call
type RunResult struct {
	Stdout   *bytes.Buffer
	Stderr   *bytes.Buffer
	ExitCode int
	Duration time.Duration
	Args     []string
}

// TestCase is a testcase
type TestCase struct {
	Setup    string `yaml:"setup"`
	Teardown string `yaml:"teardown"`
}

// diskConfig is a YAML config
type diskConfig struct {
	TestCases map[string]TestCase
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

	for attempts < 5000 {
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

	// maximum runtime of a test
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, *testTimeout)
	defer cancel()

	klog.Infof("starting %q iteration. initialization args: %v, cleanup args: %v", name, setup, cleanup)

	e := ExperimentResult{Name: name, Timestamp: time.Now(), Args: setup}

	rr, err := Run(exec.CommandContext(ctx, binary, "version"))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.Version = strings.Split(rr.Stdout.String(), "\n")[0]

	rr, err = Run(exec.CommandContext(ctx, binary, setup[1:]...))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.Startup = rr.Duration

	extraArgs := []string{}

	if strings.Contains(binary, "kind") {
		extraArgs = []string{"--context", "kind-kind"}
	}
	if strings.Contains(binary, "minikube") {
		extraArgs = []string{"--context", "minikube"}
	}
	if strings.Contains(binary, "k3d") {
		extraArgs = []string{"--context", "k3d-k3s-default"}
	}

	args := append(extraArgs, "get", "po", "-A")
	rr, err = RetryRun(exec.CommandContext(ctx, "kubectl", args...))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.APIAnswering = rr.Duration

	args = append(extraArgs, "get", "svc", "kubernetes")
	rr, err = RetryRun(exec.CommandContext(ctx, "kubectl", args...))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.KubernetesSvc = rr.Duration

	args = append(extraArgs, "get", "svc", "kube-dns", "-n", "kube-system")
	rr, err = RetryRun(exec.CommandContext(ctx, "kubectl", args...))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.DNSSvc = rr.Duration

	args = append(extraArgs, "apply", "-f", "manifests/netcat-svc.yaml")
	rr, err = RetryRun(exec.CommandContext(ctx, "kubectl", args...))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.AppRunning = rr.Duration

	args = append(extraArgs, "exec", "deployment/netcat", "--", "nc", "-v", "localhost", "8080")
	rr, err = RetryRun(exec.CommandContext(ctx, "kubectl", args...))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.AppRunning += rr.Duration

	args = append(extraArgs, "exec", "deployment/netcat", "--", "nslookup", "netcat.default")
	rr, err = RetryRun(exec.CommandContext(ctx, "kubectl", args...))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}
	e.DNSAnswering = rr.Duration

	e.Total = e.Startup + e.APIAnswering + e.KubernetesSvc + e.DNSSvc + e.AppRunning + e.DNSAnswering

	rr, err = RetryRun(exec.Command(cleanup[0], cleanup[1:]...))
	if err != nil {
		e.ExitCode = rr.ExitCode
		return e, fmt.Errorf("%s failed: %w", rr, err)
	}

	return e, nil
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	if *configPath == "" {
		klog.Exitf("--config is a required flag. See ./local-kubernetes.yaml, for example")
	}
	f, err := ioutil.ReadFile(*configPath)
	if err != nil {
		klog.Exitf("unable to read config: %v", err)
	}

	dc := &diskConfig{}
	err = yaml.Unmarshal(f, &dc)
	if err != nil {
		klog.Exitf("unmarshal: %w", err)
	}

	tf, err := ioutil.TempFile("", filepath.Base(*configPath)+".*.csv")
	if err != nil {
		klog.Exitf("tempfile: %v", err)
	}

	c := csv.NewWriter(tf)

	c.Write([]string{"name", "args", "platform", "iteration", "time", "version", "exitcode", "error", "startup (seconds)", "apiserver answering (seconds)", "kubernetes svc (seconds)", "dns svc (seconds)", "app running (seconds)", "dns answering (seconds)", "total duration (seconds)"})
	klog.Infof("Writing output to %s", tf.Name())
	c.Flush()

	// quick cleanup loop
	for name, tc := range dc.TestCases {
		cleanup := strings.Split(tc.Teardown, " ")
		klog.Infof("cleaning up %q with arguments: %v", name, cleanup)
		Run(exec.Command(cleanup[0], cleanup[1:]...))
	}

	for i := 0; i <= *iterationCount; i++ {
		if i == 0 {
			klog.Infof("Starting dry-run iteration - will not record results")
		} else {
			klog.Infof("STARTING ITERATION COUNT %d of %d", i, *iterationCount)
		}

		for name, tc := range dc.TestCases {
			e, err := runIteration(name, tc.Setup, tc.Teardown)
			if err != nil {
				e.Error = err.Error()
				if i == 0 {
					klog.Exitf("%s dry-run failed: %v", name, err)
				}
				klog.Errorf("%s experiment failed: %v", name, err)
			}
			klog.Infof("%s#%d took %s: %+v", name, i, e.Total, e)
			if i == 0 {
				continue
			}
			klog.Infof("Updating %s ...", tf.Name())
			fields := []string{
				name,
				strings.Join(e.Args, " "),
				runtime.GOOS,
				fmt.Sprintf("%d", i),
				e.Timestamp.String(),
				e.Version,
				string(e.ExitCode),
				e.Error,
				ds(e.Startup),
				ds(e.APIAnswering),
				ds(e.KubernetesSvc),
				ds(e.DNSSvc),
				ds(e.AppRunning),
				ds(e.DNSAnswering),
				ds(e.Total),
			}
			c.Write(fields)
			c.Flush()
		}
	}

}
