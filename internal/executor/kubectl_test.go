package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	k8sclient "github.com/jimytar/aiops-agent/internal/k8s"
)

func newFakeExecutor(cs kubernetes.Interface) *KubectlExecutor {
	clients := k8sclient.NewFakeClients(map[string]kubernetes.Interface{
		"test": cs,
	})
	return NewKubectlExecutor(clients, []string{"env", "ls", "cat ", "ps aux"})
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestKubectlGetPods(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(pod))

	out, err := e.Get(context.Background(), "pods", "", "default", "test")
	if err != nil {
		t.Fatalf("Get pods: %v", err)
	}
	if !strings.Contains(out, "web-abc") {
		t.Errorf("Get pods output missing pod name, got:\n%s", out)
	}
}

func TestKubectlGetDeployments(t *testing.T) {
	var replicas int32 = 2
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 2, Replicas: 2},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(dep))

	out, err := e.Get(context.Background(), "deployments", "api", "prod", "test")
	if err != nil {
		t.Fatalf("Get deployments: %v", err)
	}
	if !strings.Contains(out, "api") {
		t.Errorf("Get deployments missing name, got:\n%s", out)
	}
}

func TestKubectlGetUnknownCluster(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	_, err := e.Get(context.Background(), "pods", "", "default", "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown cluster")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention cluster name, got: %v", err)
	}
}

func TestKubectlGetUnknownResource(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	_, err := e.Get(context.Background(), "widgets", "", "default", "test")
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}

func TestKubectlGetServices(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "default"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.1"},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(svc))

	out, err := e.Get(context.Background(), "services", "", "default", "test")
	if err != nil {
		t.Fatalf("Get services: %v", err)
	}
	if !strings.Contains(out, "frontend") {
		t.Errorf("Get services missing name, got:\n%s", out)
	}
}

func TestKubectlGetNamespaces(t *testing.T) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "production"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(ns))

	out, err := e.Get(context.Background(), "namespaces", "", "", "test")
	if err != nil {
		t.Fatalf("Get namespaces: %v", err)
	}
	if !strings.Contains(out, "production") {
		t.Errorf("Get namespaces missing name, got:\n%s", out)
	}
}

func TestKubectlGetNodes(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.33.0"},
		},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(node))

	out, err := e.Get(context.Background(), "nodes", "", "", "test")
	if err != nil {
		t.Fatalf("Get nodes: %v", err)
	}
	if !strings.Contains(out, "node-1") {
		t.Errorf("Get nodes missing name, got:\n%s", out)
	}
	if !strings.Contains(out, "Ready") {
		t.Errorf("Get nodes missing Ready status, got:\n%s", out)
	}
}

func TestKubectlGetStatefulSets(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "default"},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1, Replicas: 1},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(sts))

	out, err := e.Get(context.Background(), "statefulsets", "", "default", "test")
	if err != nil {
		t.Fatalf("Get statefulsets: %v", err)
	}
	if !strings.Contains(out, "postgres") {
		t.Errorf("Get statefulsets missing name, got:\n%s", out)
	}
}

func TestKubectlGetByName(t *testing.T) {
	pod1 := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "default"}}
	pod2 := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-2", Namespace: "default"}}
	e := newFakeExecutor(fake.NewSimpleClientset(pod1, pod2))

	out, err := e.Get(context.Background(), "pods", "web-1", "default", "test")
	if err != nil {
		t.Fatalf("Get pods by name: %v", err)
	}
	if !strings.Contains(out, "web-1") {
		t.Errorf("expected web-1 in output, got:\n%s", out)
	}
}

func TestKubectlGetEmpty(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	out, err := e.Get(context.Background(), "pods", "", "default", "test")
	if err != nil {
		t.Fatalf("Get empty pods: %v", err)
	}
	// Header is always written; no data rows when empty.
	if !strings.Contains(out, "NAME") {
		t.Errorf("expected header row, got:\n%s", out)
	}
}

// ── Logs ──────────────────────────────────────────────────────────────────────

func TestKubectlLogsUnknownPod(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	// fake client returns empty logs for unknown pods rather than an error,
	// so just check it doesn't panic and returns something.
	_, err := e.Logs(context.Background(), "ghost", "default", "test", "", 50)
	// fake client may or may not error — we just verify it doesn't hang.
	_ = err
}

// ── Scale ─────────────────────────────────────────────────────────────────────

func TestKubectlScale(t *testing.T) {
	var replicas int32 = 3
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(dep))

	out, err := e.Scale(context.Background(), "worker", "default", "test", 1)
	if err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if !strings.Contains(out, "worker") {
		t.Errorf("Scale output missing deployment name, got:\n%s", out)
	}
}

func TestKubectlScaleUnknownDeployment(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	_, err := e.Scale(context.Background(), "ghost", "default", "test", 2)
	if err == nil {
		t.Fatal("expected error scaling nonexistent deployment")
	}
}

// ── Restart ───────────────────────────────────────────────────────────────────

func TestKubectlRestart(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(dep))

	out, err := e.Restart(context.Background(), "app", "default", "test")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !strings.Contains(out, "app") {
		t.Errorf("Restart output missing deployment name, got:\n%s", out)
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestKubectlDeletePod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: "default"},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(pod))

	out, err := e.Delete(context.Background(), "pods", "stale", "default", "test")
	if err != nil {
		t.Fatalf("Delete pod: %v", err)
	}
	if !strings.Contains(out, "stale") {
		t.Errorf("Delete output missing pod name, got:\n%s", out)
	}
}

func TestKubectlDeleteUnsupportedResource(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	_, err := e.Delete(context.Background(), "configmaps", "cm", "default", "test")
	if err == nil {
		t.Fatal("expected error for unsupported resource type")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error should mention unsupported, got: %v", err)
	}
}

// ── GetEvents ─────────────────────────────────────────────────────────────────

func TestKubectlGetEventsEmpty(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	out, err := e.GetEvents(context.Background(), "default", "test")
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if !strings.Contains(out, "No events") {
		t.Errorf("expected 'No events', got:\n%s", out)
	}
}

func TestKubectlGetEventsWithEvent(t *testing.T) {
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ev1", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web"},
		Reason:         "OOMKilled",
		Message:        "container exceeded memory limit",
		Type:           corev1.EventTypeWarning,
	}
	e := newFakeExecutor(fake.NewSimpleClientset(ev))

	out, err := e.GetEvents(context.Background(), "default", "test")
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if !strings.Contains(out, "OOMKilled") {
		t.Errorf("GetEvents missing event reason, got:\n%s", out)
	}
}

// ── Describe ──────────────────────────────────────────────────────────────────

func TestKubectlDescribePod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName:   "node-1",
			Containers: []corev1.Container{{Name: "app", Image: "nginx:latest"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(pod))

	out, err := e.Describe(context.Background(), "pods", "web-abc", "default", "test")
	if err != nil {
		t.Fatalf("Describe pod: %v", err)
	}
	for _, want := range []string{"web-abc", "node-1", "app", "nginx:latest"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe pod missing %q, got:\n%s", want, out)
		}
	}
}

func TestKubectlDescribePodMissingName(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	_, err := e.Describe(context.Background(), "pods", "", "default", "test")
	if err == nil {
		t.Fatal("expected error when name is empty")
	}
}

func TestKubectlDescribeDeployment(t *testing.T) {
	var replicas int32 = 2
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "api", Image: "myapp:v1"}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2},
	}
	e := newFakeExecutor(fake.NewSimpleClientset(dep))

	out, err := e.Describe(context.Background(), "deployments", "api", "default", "test")
	if err != nil {
		t.Fatalf("Describe deployment: %v", err)
	}
	for _, want := range []string{"api", "myapp:v1"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe deployment missing %q, got:\n%s", want, out)
		}
	}
}

func TestKubectlDescribeUnsupported(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	_, err := e.Describe(context.Background(), "services", "svc", "default", "test")
	if err == nil {
		t.Fatal("expected error for unsupported describe resource")
	}
}

// ── Exec allowlist ────────────────────────────────────────────────────────────

func TestKubectlExecAllowlistRejects(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	_, err := e.Exec(context.Background(), "pod", "default", "", "test", "rm -rf /")
	if err == nil {
		t.Fatal("Exec should reject command not in allowlist")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("error should mention allowlist, got: %v", err)
	}
}

func TestKubectlExecAllowlistAccepts(t *testing.T) {
	e := newFakeExecutor(fake.NewSimpleClientset())
	// Command passes the allowlist check but will fail on exec (no real pod).
	// We only care that the allowlist doesn't block it.
	_, err := e.Exec(context.Background(), "pod", "default", "", "test", "env")
	// Error is expected (no real pod/cluster), but NOT an allowlist error.
	if err != nil && strings.Contains(err.Error(), "allowlist") {
		t.Errorf("'env' should pass allowlist check, got: %v", err)
	}
}

// ── age helper ────────────────────────────────────────────────────────────────

func TestAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		d    time.Duration
		want string // suffix character
	}{
		{10 * time.Second, "s"},
		{90 * time.Second, "m"},
		{3 * time.Hour, "h"},
		{48 * time.Hour, "d"},
	}
	for _, c := range cases {
		got := age(now.Add(-c.d))
		if !strings.HasSuffix(got, c.want) {
			t.Errorf("age(-%v) = %q, want suffix %q", c.d, got, c.want)
		}
	}
}

// ── podReadyCount ─────────────────────────────────────────────────────────────

func TestPodReadyCount(t *testing.T) {
	pod := corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true},
				{Name: "sidecar", Ready: false},
				{Name: "proxy", Ready: true},
			},
		},
	}
	if got := podReadyCount(pod); got != 2 {
		t.Errorf("podReadyCount = %d, want 2", got)
	}
}

func TestPodReadyCountEmpty(t *testing.T) {
	if got := podReadyCount(corev1.Pod{}); got != 0 {
		t.Errorf("podReadyCount(empty) = %d, want 0", got)
	}
}

// ── podRestarts ───────────────────────────────────────────────────────────────

func TestPodRestarts(t *testing.T) {
	pod := corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", RestartCount: 3},
				{Name: "sidecar", RestartCount: 1},
			},
		},
	}
	if got := podRestarts(pod); got != 4 {
		t.Errorf("podRestarts = %d, want 4", got)
	}
}

func TestPodRestartsEmpty(t *testing.T) {
	if got := podRestarts(corev1.Pod{}); got != 0 {
		t.Errorf("podRestarts(empty) = %d, want 0", got)
	}
}

// ── describePod ───────────────────────────────────────────────────────────────

func TestDescribePodContainerStatuses(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName:   "node-1",
			Containers: []corev1.Container{{Name: "app", Image: "nginx:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true, RestartCount: 2},
			},
		},
	}
	out := describePod(pod)

	for _, want := range []string{"web", "default", "node-1", "app", "nginx:latest", "ready=true", "restarts=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("describePod missing %q, got:\n%s", want, out)
		}
	}
}

func TestDescribePodWaitingContainer(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "crash", Namespace: "default"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "app",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{
							Reason:  "CrashLoopBackOff",
							Message: "back-off 5m0s restarting failed container",
						},
					},
				},
			},
		},
	}
	out := describePod(pod)
	if !strings.Contains(out, "CrashLoopBackOff") {
		t.Errorf("describePod should show Waiting reason, got:\n%s", out)
	}
}

func TestDescribePodTerminatedContainer(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "default"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "worker",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Reason:   "OOMKilled",
							ExitCode: 137,
						},
					},
				},
			},
		},
	}
	out := describePod(pod)
	if !strings.Contains(out, "OOMKilled") {
		t.Errorf("describePod should show Terminated reason, got:\n%s", out)
	}
	if !strings.Contains(out, "137") {
		t.Errorf("describePod should show exit code 137, got:\n%s", out)
	}
}
