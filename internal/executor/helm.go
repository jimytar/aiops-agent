package executor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jimytar/aiops-agent/internal/config"
)

type HelmExecutor struct {
	kubeconfigDir string
	clusters      []config.ClusterConfig
}

func NewHelmExecutor(cfg *config.Config) *HelmExecutor {
	return &HelmExecutor{
		kubeconfigDir: cfg.KubeconfigDir,
		clusters:      cfg.Clusters,
	}
}

func (e *HelmExecutor) List(namespace, cluster string) (string, error) {
	args := []string{"list", "--all-namespaces"}
	if namespace != "" {
		args = []string{"list", "--namespace", namespace}
	}
	return e.run(cluster, args...)
}

func (e *HelmExecutor) Status(release, namespace, cluster string) (string, error) {
	args := []string{"status", release}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	}
	return e.run(cluster, args...)
}

func (e *HelmExecutor) Rollback(release, namespace, cluster string, revision int) (string, error) {
	args := []string{"rollback", release}
	if revision > 0 {
		args = append(args, fmt.Sprintf("%d", revision))
	}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	}
	return e.run(cluster, args...)
}

func (e *HelmExecutor) run(cluster string, args ...string) (string, error) {
	var kubeconfigPath string
	found := false
	for _, c := range e.clusters {
		if c.Name == cluster {
			found = true
			if !c.InCluster {
				f := c.KubeconfigFile
				if f == "" {
					f = c.Name
				}
				kubeconfigPath = filepath.Join(e.kubeconfigDir, f)
			}
			break
		}
	}
	if !found && cluster != "" {
		return "", fmt.Errorf("unknown cluster %q", cluster)
	}

	// Build env, stripping any existing KUBECONFIG so our value always wins.
	baseEnv := filterEnv(os.Environ(), "KUBECONFIG")
	var env []string
	if kubeconfigPath != "" {
		env = append(baseEnv, "KUBECONFIG="+kubeconfigPath)
	} else {
		env = baseEnv
	}

	cmd := exec.Command("helm", args...) //nolint:gosec // args are hardcoded, not user-supplied
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return stdout.String(), fmt.Errorf("helm %s: %s", strings.Join(args, " "), errMsg)
	}
	return stdout.String(), nil
}

// filterEnv returns a copy of env with all entries for the given key removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
