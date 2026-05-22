package executor

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	k8sclient "github.com/jimytar/aiops-agent/internal/k8s"
)

type KubectlExecutor struct {
	clients         *k8sclient.Clients
	execAllowedCmds []string
}

func NewKubectlExecutor(clients *k8sclient.Clients, execAllowedCmds []string) *KubectlExecutor {
	return &KubectlExecutor{clients: clients, execAllowedCmds: execAllowedCmds}
}

// ClientFor returns the kubernetes client for the named cluster (used by health checks).
func (e *KubectlExecutor) ClientFor(cluster string) (kubernetes.Interface, error) {
	return e.client(cluster)
}

func (e *KubectlExecutor) client(cluster string) (kubernetes.Interface, error) {
	return e.clients.Get(cluster)
}

func (e *KubectlExecutor) Get(ctx context.Context, resource, name, namespace, cluster string) (string, error) {
	cs, err := e.client(cluster)
	if err != nil {
		return "", err
	}
	ns := nsOrDefault(namespace)

	var buf bytes.Buffer

	switch strings.ToLower(resource) {
	case "pods", "pod", "po":
		list, err := cs.CoreV1().Pods(ns).List(ctx, listOpts(name))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&buf, "%-50s %-10s %-6s %-10s %s\n", "NAME", "READY", "STATUS", "RESTARTS", "AGE")
		for _, p := range list.Items {
			ready := podReadyCount(p)
			total := len(p.Spec.Containers)
			restarts := podRestarts(p)
			fmt.Fprintf(&buf, "%-50s %d/%-8d %-6s %-10d %s\n",
				p.Name, ready, total,
				string(p.Status.Phase), restarts,
				age(p.CreationTimestamp.Time))
		}

	case "deployments", "deployment", "deploy":
		list, err := cs.AppsV1().Deployments(ns).List(ctx, listOpts(name))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&buf, "%-50s %-7s %-7s %-7s %s\n", "NAME", "READY", "UP-TO-DATE", "AVAILABLE", "AGE")
		for _, d := range list.Items {
			fmt.Fprintf(&buf, "%-50s %d/%-5d %-7d %-9d %s\n",
				d.Name,
				d.Status.ReadyReplicas, d.Status.Replicas,
				d.Status.UpdatedReplicas, d.Status.AvailableReplicas,
				age(d.CreationTimestamp.Time))
		}

	case "services", "service", "svc":
		list, err := cs.CoreV1().Services(ns).List(ctx, listOpts(name))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&buf, "%-40s %-10s %-20s %s\n", "NAME", "TYPE", "CLUSTER-IP", "AGE")
		for _, s := range list.Items {
			fmt.Fprintf(&buf, "%-40s %-10s %-20s %s\n",
				s.Name, string(s.Spec.Type), s.Spec.ClusterIP,
				age(s.CreationTimestamp.Time))
		}

	case "namespaces", "namespace", "ns":
		list, err := cs.CoreV1().Namespaces().List(ctx, listOpts(name))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&buf, "%-40s %-10s %s\n", "NAME", "STATUS", "AGE")
		for _, n := range list.Items {
			fmt.Fprintf(&buf, "%-40s %-10s %s\n",
				n.Name, string(n.Status.Phase),
				age(n.CreationTimestamp.Time))
		}

	case "nodes", "node", "no":
		list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&buf, "%-30s %-10s %-20s %s\n", "NAME", "STATUS", "VERSION", "AGE")
		for _, n := range list.Items {
			status := "NotReady"
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
					status = "Ready"
				}
			}
			fmt.Fprintf(&buf, "%-30s %-10s %-20s %s\n",
				n.Name, status, n.Status.NodeInfo.KubeletVersion,
				age(n.CreationTimestamp.Time))
		}

	case "statefulsets", "statefulset", "sts":
		list, err := cs.AppsV1().StatefulSets(ns).List(ctx, listOpts(name))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&buf, "%-50s %-7s %s\n", "NAME", "READY", "AGE")
		for _, s := range list.Items {
			fmt.Fprintf(&buf, "%-50s %d/%-5d %s\n",
				s.Name, s.Status.ReadyReplicas, s.Status.Replicas,
				age(s.CreationTimestamp.Time))
		}

	default:
		return "", fmt.Errorf("unsupported resource type %q", resource)
	}

	return buf.String(), nil
}

func (e *KubectlExecutor) Describe(ctx context.Context, resource, name, namespace, cluster string) (string, error) {
	cs, err := e.client(cluster)
	if err != nil {
		return "", err
	}
	ns := nsOrDefault(namespace)

	switch strings.ToLower(resource) {
	case "pods", "pod", "po":
		if name == "" {
			return "", fmt.Errorf("name is required for describe pod")
		}
		p, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		return describePod(p), nil

	case "deployments", "deployment", "deploy":
		if name == "" {
			return "", fmt.Errorf("name is required for describe deployment")
		}
		d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "Name:      %s\nNamespace: %s\nAge:       %s\n",
			d.Name, d.Namespace, age(d.CreationTimestamp.Time))
		fmt.Fprintf(&buf, "Replicas:  %d desired / %d ready / %d available\n",
			d.Status.Replicas, d.Status.ReadyReplicas, d.Status.AvailableReplicas)
		fmt.Fprintf(&buf, "Strategy:  %s\n", d.Spec.Strategy.Type)
		for k, v := range d.Labels {
			fmt.Fprintf(&buf, "Label:     %s=%s\n", k, v)
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			fmt.Fprintf(&buf, "Container: %s image=%s\n", c.Name, c.Image)
		}
		for _, c := range d.Status.Conditions {
			fmt.Fprintf(&buf, "Condition: %s=%s reason=%s\n", c.Type, c.Status, c.Reason)
		}
		return buf.String(), nil

	default:
		return "", fmt.Errorf("describe not supported for %q, use get instead", resource)
	}
}

func (e *KubectlExecutor) Logs(ctx context.Context, pod, namespace, cluster, container string, tailLines int64) (string, error) {
	cs, err := e.client(cluster)
	if err != nil {
		return "", err
	}
	ns := nsOrDefault(namespace)

	opts := &corev1.PodLogOptions{}
	if tailLines > 0 {
		opts.TailLines = &tailLines
	}
	if container != "" {
		opts.Container = container
	}

	req := cs.CoreV1().Pods(ns).GetLogs(pod, opts)
	logs, err := req.DoRaw(ctx)
	if err != nil {
		return "", fmt.Errorf("get logs for pod %s/%s: %w", ns, pod, err)
	}
	return string(logs), nil
}

func (e *KubectlExecutor) GetEvents(ctx context.Context, namespace, cluster string) (string, error) {
	cs, err := e.client(cluster)
	if err != nil {
		return "", err
	}
	ns := nsOrDefault(namespace)

	events, err := cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	if len(events.Items) == 0 {
		return "No events found.", nil
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%-10s %-10s %-30s %-30s %s\n", "LAST SEEN", "TYPE", "REASON", "OBJECT", "MESSAGE")
	for _, ev := range events.Items {
		last := "unknown"
		if !ev.LastTimestamp.IsZero() {
			last = age(ev.LastTimestamp.Time)
		}
		obj := fmt.Sprintf("%s/%s", ev.InvolvedObject.Kind, ev.InvolvedObject.Name)
		msg := ev.Message
		if len(msg) > 60 {
			msg = msg[:60] + "..."
		}
		fmt.Fprintf(&buf, "%-10s %-10s %-30s %-30s %s\n",
			last, ev.Type, ev.Reason, obj, msg)
	}
	return buf.String(), nil
}

func (e *KubectlExecutor) Restart(ctx context.Context, deployment, namespace, cluster string) (string, error) {
	cs, err := e.client(cluster)
	if err != nil {
		return "", err
	}
	ns := nsOrDefault(namespace)

	// Rolling restart via patch annotation.
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))
	_, err = cs.AppsV1().Deployments(ns).Patch(ctx, deployment, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return "", fmt.Errorf("patch deployment %s/%s: %w", ns, deployment, err)
	}
	return fmt.Sprintf("Deployment %s/%s rolling restart triggered.", ns, deployment), nil
}

func (e *KubectlExecutor) Scale(ctx context.Context, deployment, namespace, cluster string, replicas int32) (string, error) {
	cs, err := e.client(cluster)
	if err != nil {
		return "", err
	}
	ns := nsOrDefault(namespace)

	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	_, err = cs.AppsV1().Deployments(ns).Patch(ctx, deployment, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return "", fmt.Errorf("scale deployment %s/%s: %w", ns, deployment, err)
	}
	return fmt.Sprintf("Deployment %s/%s scaled to %d replicas.", ns, deployment, replicas), nil
}

func (e *KubectlExecutor) Delete(ctx context.Context, resource, name, namespace, cluster string) (string, error) {
	cs, err := e.client(cluster)
	if err != nil {
		return "", err
	}
	ns := nsOrDefault(namespace)

	switch strings.ToLower(resource) {
	case "pods", "pod", "po":
		err = cs.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{})
	case "deployments", "deployment", "deploy":
		err = cs.AppsV1().Deployments(ns).Delete(ctx, name, metav1.DeleteOptions{})
	default:
		return "", fmt.Errorf("delete not supported for resource type %q", resource)
	}
	if err != nil {
		return "", fmt.Errorf("delete %s %s/%s: %w", resource, ns, name, err)
	}
	return fmt.Sprintf("Deleted %s %s/%s.", resource, ns, name), nil
}

func (e *KubectlExecutor) Exec(ctx context.Context, pod, namespace, container, cluster, command string) (string, error) {
	for _, prefix := range e.execAllowedCmds {
		if strings.HasPrefix(command, prefix) {
			goto allowed
		}
	}
	return "", fmt.Errorf("command not in exec allowlist: %q", command)
allowed:

	cs, err := e.client(cluster)
	if err != nil {
		return "", err
	}
	restCfg, err := e.clients.GetRestConfig(cluster)
	if err != nil {
		return "", err
	}
	ns := nsOrDefault(namespace)

	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command:   []string{"sh", "-c", command},
			Container: container,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	execStream, err := remotecommand.NewSPDYExecutor(restCfg, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("exec setup: %w", err)
	}

	var stdout, stderr bytes.Buffer
	if err := execStream.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		out := stdout.String()
		if se := stderr.String(); se != "" {
			out += "\nSTDERR:\n" + se
		}
		return out, fmt.Errorf("exec: %w", err)
	}
	out := stdout.String()
	if se := stderr.String(); se != "" {
		out += "\nSTDERR:\n" + se
	}
	return out, nil
}

// helpers

func nsOrDefault(ns string) string {
	if ns == "" {
		return metav1.NamespaceAll
	}
	return ns
}

func listOpts(name string) metav1.ListOptions {
	if name != "" {
		return metav1.ListOptions{FieldSelector: "metadata.name=" + name}
	}
	return metav1.ListOptions{}
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func podReadyCount(p corev1.Pod) int {
	n := 0
	for _, c := range p.Status.ContainerStatuses {
		if c.Ready {
			n++
		}
	}
	return n
}

func podRestarts(p corev1.Pod) int32 {
	var n int32
	for _, c := range p.Status.ContainerStatuses {
		n += c.RestartCount
	}
	return n
}

func describePod(p *corev1.Pod) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Name:      %s\nNamespace: %s\nNode:      %s\nAge:       %s\nPhase:     %s\n",
		p.Name, p.Namespace, p.Spec.NodeName,
		age(p.CreationTimestamp.Time), string(p.Status.Phase))
	for _, c := range p.Status.Conditions {
		fmt.Fprintf(&buf, "Condition: %s=%s\n", c.Type, c.Status)
	}
	for _, c := range p.Spec.Containers {
		fmt.Fprintf(&buf, "Container: %s image=%s\n", c.Name, c.Image)
	}
	for _, cs := range p.Status.ContainerStatuses {
		fmt.Fprintf(&buf, "  %s: ready=%v restarts=%d\n", cs.Name, cs.Ready, cs.RestartCount)
		if cs.State.Waiting != nil {
			fmt.Fprintf(&buf, "    Waiting: %s %s\n", cs.State.Waiting.Reason, cs.State.Waiting.Message)
		}
		if cs.State.Terminated != nil {
			fmt.Fprintf(&buf, "    Terminated: %s exitCode=%d\n", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
		}
	}
	return buf.String()
}
