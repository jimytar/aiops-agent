package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	k8sclient "github.com/jimytar/aiops-agent/internal/k8s"
)

// FluxExecutor triggers Flux reconciliations via the CRD annotation patch.
type FluxExecutor struct {
	clients *k8sclient.Clients
}

func NewFluxExecutor(clients *k8sclient.Clients) *FluxExecutor {
	return &FluxExecutor{clients: clients}
}

var fluxGVRs = map[string]schema.GroupVersionResource{
	"kustomization": {Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"},
	"helmrelease":   {Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
}

func (e *FluxExecutor) Reconcile(ctx context.Context, kind, name, namespace, cluster string) (string, error) {
	gvr, ok := fluxGVRs[strings.ToLower(kind)]
	if !ok {
		return "", fmt.Errorf("unsupported flux kind %q; use kustomization or helmrelease", kind)
	}
	ns := namespace
	if ns == "" {
		ns = "flux-system"
	}

	dc, err := e.clients.GetDynamic(cluster)
	if err != nil {
		return "", err
	}

	patch := fmt.Sprintf(`{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":%q}}}`,
		time.Now().UTC().Format(time.RFC3339))
	_, err = dc.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return "", fmt.Errorf("patch %s %s/%s: %w", kind, ns, name, err)
	}
	return fmt.Sprintf("Triggered reconciliation of %s %s/%s on cluster %s.", kind, ns, name, cluster), nil
}
