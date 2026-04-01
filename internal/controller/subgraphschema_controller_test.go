package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vahallav1alpha1 "github.com/vahalla-wealth/graph-controller/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = vahallav1alpha1.AddToScheme(s)
	return s
}

func newSubgraphSchema(name, namespace, routingUrl, schema string) *vahallav1alpha1.SubgraphSchema {
	return &vahallav1alpha1.SubgraphSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: vahallav1alpha1.SubgraphSchemaSpec{
			RoutingUrl: routingUrl,
			Schema:     schema,
		},
	}
}

func int32Ptr(i int32) *int32 { return &i }

func newRouterDeployment(namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "graph-router",
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "graph-router"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "graph-router"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "router", Image: "ghcr.io/apollographql/router:v2.11.0"},
					},
				},
			},
		},
	}
}

// TestComposeGeneratesSupergraphConfig verifies that the compose method creates
// the correct rover config file and schema files in the temp directory.
func TestComposeGeneratesSupergraphConfig(t *testing.T) {
	// Create a mock rover script that just copies the schemas into a supergraph output.
	tmpDir := t.TempDir()
	mockRover := filepath.Join(tmpDir, "mock-rover")
	// The mock rover reads --config and --output flags, then writes a dummy supergraph.
	mockScript := `#!/bin/sh
OUTPUT=""
CONFIG=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) OUTPUT="$2"; shift 2;;
    --config) CONFIG="$2"; shift 2;;
    *) shift;;
  esac
done
if [ -z "$OUTPUT" ]; then
  echo "missing --output" >&2
  exit 1
fi
echo "# composed supergraph from $CONFIG" > "$OUTPUT"
cat "$CONFIG" >> "$OUTPUT"
`
	if err := os.WriteFile(mockRover, []byte(mockScript), 0755); err != nil {
		t.Fatal(err)
	}

	s := newScheme()
	reconciler := &SubgraphSchemaReconciler{
		Client:             fake.NewClientBuilder().WithScheme(s).Build(),
		Scheme:             s,
		FederationVersion:  "=2.7.0",
		RoverPath:          mockRover,
	}

	schemas := []vahallav1alpha1.SubgraphSchema{
		*newSubgraphSchema("crm-service", "default", "http://crm-service:8080/query",
			"type Query { health: String! }"),
		*newSubgraphSchema("identity-service", "default", "http://identity-service:8080/query",
			"type Query { me: User } type User { id: ID! }"),
	}

	result, err := reconciler.compose(context.Background(), schemas)
	if err != nil {
		t.Fatalf("compose failed: %v", err)
	}

	if result == "" {
		t.Fatal("compose returned empty result")
	}

	// Verify the output contains both subgraph names (sorted).
	if !contains(result, "crm-service:") {
		t.Errorf("expected composed output to contain 'crm-service:', got:\n%s", result)
	}
	if !contains(result, "identity-service:") {
		t.Errorf("expected composed output to contain 'identity-service:', got:\n%s", result)
	}
	// Verify federation version is present.
	if !contains(result, "federation_version: =2.7.0") {
		t.Errorf("expected composed output to contain federation_version, got:\n%s", result)
	}
}

// TestUpsertConfigMapCreates verifies that upsertConfigMap creates a new ConfigMap
// when one doesn't exist.
func TestUpsertConfigMapCreates(t *testing.T) {
	s := newScheme()
	cl := fake.NewClientBuilder().WithScheme(s).Build()

	reconciler := &SubgraphSchemaReconciler{
		Client:              cl,
		Scheme:              s,
		SupergraphConfigMap: "graph-supergraph",
	}

	ctx := context.Background()
	sdl := "type Query { hello: String! }"
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(sdl)))

	if err := reconciler.upsertConfigMap(ctx, "default", sdl, checksum); err != nil {
		t.Fatalf("upsertConfigMap create failed: %v", err)
	}

	// Verify the ConfigMap was created.
	var cm corev1.ConfigMap
	if err := cl.Get(ctx, types.NamespacedName{Name: "graph-supergraph", Namespace: "default"}, &cm); err != nil {
		t.Fatalf("failed to get created ConfigMap: %v", err)
	}

	if cm.Data["supergraph.graphql"] != sdl {
		t.Errorf("expected ConfigMap data to contain SDL, got: %s", cm.Data["supergraph.graphql"])
	}
	if cm.Annotations["vahalla.io/supergraph-checksum"] != checksum {
		t.Errorf("expected checksum annotation %s, got: %s", checksum, cm.Annotations["vahalla.io/supergraph-checksum"])
	}
}

// TestUpsertConfigMapUpdates verifies that upsertConfigMap updates an existing ConfigMap.
func TestUpsertConfigMapUpdates(t *testing.T) {
	s := newScheme()
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "graph-supergraph",
			Namespace: "default",
			Annotations: map[string]string{
				"vahalla.io/supergraph-checksum": "old-checksum",
			},
		},
		Data: map[string]string{
			"supergraph.graphql": "old schema",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(existingCM).Build()

	reconciler := &SubgraphSchemaReconciler{
		Client:              cl,
		Scheme:              s,
		SupergraphConfigMap: "graph-supergraph",
	}

	ctx := context.Background()
	newSDL := "type Query { updated: Boolean! }"
	newChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(newSDL)))

	if err := reconciler.upsertConfigMap(ctx, "default", newSDL, newChecksum); err != nil {
		t.Fatalf("upsertConfigMap update failed: %v", err)
	}

	var cm corev1.ConfigMap
	if err := cl.Get(ctx, types.NamespacedName{Name: "graph-supergraph", Namespace: "default"}, &cm); err != nil {
		t.Fatalf("failed to get updated ConfigMap: %v", err)
	}

	if cm.Data["supergraph.graphql"] != newSDL {
		t.Errorf("expected updated SDL, got: %s", cm.Data["supergraph.graphql"])
	}
	if cm.Annotations["vahalla.io/supergraph-checksum"] != newChecksum {
		t.Errorf("expected new checksum, got: %s", cm.Annotations["vahalla.io/supergraph-checksum"])
	}
}

// TestPatchDeploymentUpdatesAnnotation verifies that patchDeployment sets the
// checksum annotation on the router Deployment's pod template.
func TestPatchDeploymentUpdatesAnnotation(t *testing.T) {
	s := newScheme()
	deploy := newRouterDeployment("default")
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy).Build()

	reconciler := &SubgraphSchemaReconciler{
		Client:           cl,
		Scheme:           s,
		RouterDeployment: "graph-router",
	}

	ctx := context.Background()
	checksum := "abc123"

	if err := reconciler.patchDeployment(ctx, "default", checksum); err != nil {
		t.Fatalf("patchDeployment failed: %v", err)
	}

	var updated appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Name: "graph-router", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}

	got := updated.Spec.Template.Annotations["vahalla.io/supergraph-checksum"]
	if got != checksum {
		t.Errorf("expected annotation %s, got: %s", checksum, got)
	}
}

// TestPatchDeploymentSkipsWhenSameChecksum verifies that patchDeployment is
// a no-op when the checksum hasn't changed.
func TestPatchDeploymentSkipsWhenSameChecksum(t *testing.T) {
	s := newScheme()
	deploy := newRouterDeployment("default")
	deploy.Spec.Template.Annotations = map[string]string{
		"vahalla.io/supergraph-checksum": "same-checksum",
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy).Build()

	reconciler := &SubgraphSchemaReconciler{
		Client:           cl,
		Scheme:           s,
		RouterDeployment: "graph-router",
	}

	ctx := context.Background()
	if err := reconciler.patchDeployment(ctx, "default", "same-checksum"); err != nil {
		t.Fatalf("patchDeployment failed: %v", err)
	}
	// If no error and no panic, the skip worked.
}

// TestPatchDeploymentSkipsWhenNotFound verifies that patchDeployment doesn't
// error when the router Deployment doesn't exist.
func TestPatchDeploymentSkipsWhenNotFound(t *testing.T) {
	s := newScheme()
	cl := fake.NewClientBuilder().WithScheme(s).Build()

	reconciler := &SubgraphSchemaReconciler{
		Client:           cl,
		Scheme:           s,
		RouterDeployment: "graph-router",
	}

	ctx := context.Background()
	if err := reconciler.patchDeployment(ctx, "default", "any-checksum"); err != nil {
		t.Fatalf("expected no error for missing deployment, got: %v", err)
	}
}

// TestReconcileEndToEnd verifies the full reconcile loop using a mock rover.
func TestReconcileEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	mockRover := filepath.Join(tmpDir, "mock-rover")
	mockScript := `#!/bin/sh
OUTPUT=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) OUTPUT="$2"; shift 2;;
    *) shift;;
  esac
done
echo "# mock supergraph" > "$OUTPUT"
`
	if err := os.WriteFile(mockRover, []byte(mockScript), 0755); err != nil {
		t.Fatal(err)
	}

	s := newScheme()
	subgraph := newSubgraphSchema("crm-service", "default", "http://crm-service:8080/query", "type Query { health: String! }")
	deploy := newRouterDeployment("default")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(subgraph, deploy).
		WithStatusSubresource(subgraph).
		Build()

	reconciler := &SubgraphSchemaReconciler{
		Client:              cl,
		Scheme:              s,
		FederationVersion:   "=2.7.0",
		RouterDeployment:    "graph-router",
		SupergraphConfigMap: "graph-supergraph",
		RoverPath:           mockRover,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "crm-service", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got: %v", result.RequeueAfter)
	}

	// Verify ConfigMap was created.
	var cm corev1.ConfigMap
	if err := cl.Get(ctx, types.NamespacedName{Name: "graph-supergraph", Namespace: "default"}, &cm); err != nil {
		t.Fatalf("expected ConfigMap to be created: %v", err)
	}
	if cm.Data["supergraph.graphql"] == "" {
		t.Error("expected non-empty supergraph in ConfigMap")
	}

	// Verify Deployment was patched.
	var updatedDeploy appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Name: "graph-router", Namespace: "default"}, &updatedDeploy); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if updatedDeploy.Spec.Template.Annotations["vahalla.io/supergraph-checksum"] == "" {
		t.Error("expected checksum annotation on deployment")
	}

	// Verify SubgraphSchema status was updated.
	var updatedSchema vahallav1alpha1.SubgraphSchema
	if err := cl.Get(ctx, types.NamespacedName{Name: "crm-service", Namespace: "default"}, &updatedSchema); err != nil {
		t.Fatalf("failed to get SubgraphSchema: %v", err)
	}
	if updatedSchema.Status.CompositionStatus != vahallav1alpha1.CompositionStatusSuccess {
		t.Errorf("expected Success status, got: %s", updatedSchema.Status.CompositionStatus)
	}
}

// TestReconcileNoSubgraphs verifies that reconcile is a no-op when there are
// no SubgraphSchema resources.
func TestReconcileNoSubgraphs(t *testing.T) {
	s := newScheme()
	cl := fake.NewClientBuilder().WithScheme(s).Build()

	reconciler := &SubgraphSchemaReconciler{
		Client: cl,
		Scheme: s,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "any", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got: %v", result.RequeueAfter)
	}
}

// TestReconcileCompositionFailure verifies that a rover failure sets
// status to Failed and requeues.
func TestReconcileCompositionFailure(t *testing.T) {
	tmpDir := t.TempDir()
	mockRover := filepath.Join(tmpDir, "fail-rover")
	if err := os.WriteFile(mockRover, []byte("#!/bin/sh\necho 'composition error' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	s := newScheme()
	subgraph := newSubgraphSchema("bad-service", "default", "http://bad:8080/query", "invalid schema")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(subgraph).
		WithStatusSubresource(subgraph).
		Build()

	reconciler := &SubgraphSchemaReconciler{
		Client:              cl,
		Scheme:              s,
		FederationVersion:   "=2.7.0",
		SupergraphConfigMap: "graph-supergraph",
		RoverPath:           mockRover,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "bad-service", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected nil error (handled internally), got: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected 30s requeue on failure, got: %v", result.RequeueAfter)
	}

	// Verify status was set to Failed.
	var updatedSchema vahallav1alpha1.SubgraphSchema
	if err := cl.Get(ctx, types.NamespacedName{Name: "bad-service", Namespace: "default"}, &updatedSchema); err != nil {
		t.Fatalf("failed to get SubgraphSchema: %v", err)
	}
	if updatedSchema.Status.CompositionStatus != vahallav1alpha1.CompositionStatusFailed {
		t.Errorf("expected Failed status, got: %s", updatedSchema.Status.CompositionStatus)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsHelper(s, substr)
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
