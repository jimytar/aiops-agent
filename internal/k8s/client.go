package k8s

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jimytar/aiops-agent/internal/config"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Clients struct {
	clients     map[string]*kubernetes.Clientset
	dynamics    map[string]dynamic.Interface
	restConfigs map[string]*rest.Config
	clusters    []string
}

func NewClients(cfg *config.Config) (*Clients, error) {
	c := &Clients{
		clients:     make(map[string]*kubernetes.Clientset),
		dynamics:    make(map[string]dynamic.Interface),
		restConfigs: make(map[string]*rest.Config),
	}

	for _, cluster := range cfg.Clusters {
		var restCfg *rest.Config
		var err error

		if cluster.InCluster {
			restCfg, err = rest.InClusterConfig()
			if err != nil {
				return nil, fmt.Errorf("in-cluster config for %q: %w", cluster.Name, err)
			}
		} else {
			kubeconfigFile := cluster.KubeconfigFile
			if kubeconfigFile == "" {
				kubeconfigFile = cluster.Name
			}
			path := filepath.Join(cfg.KubeconfigDir, kubeconfigFile)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				// Skip clusters whose kubeconfig isn't mounted yet.
				continue
			}
			restCfg, err = clientcmd.BuildConfigFromFlags("", path)
			if err != nil {
				return nil, fmt.Errorf("kubeconfig for %q: %w", cluster.Name, err)
			}
		}

		cs, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("kubernetes client for %q: %w", cluster.Name, err)
		}

		dc, err := dynamic.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("dynamic client for %q: %w", cluster.Name, err)
		}

		c.clients[cluster.Name] = cs
		c.dynamics[cluster.Name] = dc
		c.restConfigs[cluster.Name] = restCfg
		c.clusters = append(c.clusters, cluster.Name)
	}

	return c, nil
}

func (c *Clients) Get(cluster string) (*kubernetes.Clientset, error) {
	cs, ok := c.clients[cluster]
	if !ok {
		return nil, fmt.Errorf("unknown cluster %q (available: %v)", cluster, c.clusters)
	}
	return cs, nil
}

func (c *Clients) GetDynamic(cluster string) (dynamic.Interface, error) {
	dc, ok := c.dynamics[cluster]
	if !ok {
		return nil, fmt.Errorf("unknown cluster %q (available: %v)", cluster, c.clusters)
	}
	return dc, nil
}

func (c *Clients) GetRestConfig(cluster string) (*rest.Config, error) {
	rc, ok := c.restConfigs[cluster]
	if !ok {
		return nil, fmt.Errorf("unknown cluster %q", cluster)
	}
	return rc, nil
}

func (c *Clients) ClusterNames() []string {
	return c.clusters
}
