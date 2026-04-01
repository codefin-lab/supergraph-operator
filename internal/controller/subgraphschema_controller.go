package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	vahallav1alpha1 "github.com/vahalla-wealth/graph-controller/api/v1alpha1"
)

// SubgraphSchemaReconciler reconciles SubgraphSchema objects.
type SubgraphSchemaReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	FederationVersion  string
	RouterDeployment   string
	SupergraphConfigMap string
	RoverPath          string
}

// +kubebuilder:rbac:groups=vahalla.io,resources=subgraphschemas,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=vahalla.io,resources=subgraphschemas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch

func (r *SubgraphSchemaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling SubgraphSchema", "name", req.NamespacedName)

	// 1. List all SubgraphSchema CRs in the namespace.
	var schemas vahallav1alpha1.SubgraphSchemaList
	if err := r.List(ctx, &schemas, client.InNamespace(req.Namespace)); err != nil {
		logger.Error(err, "unable to list SubgraphSchemas")
		return ctrl.Result{}, err
	}

	if len(schemas.Items) == 0 {
		logger.Info("no SubgraphSchemas found, skipping composition")
		return ctrl.Result{}, nil
	}

	// 2. Compose the supergraph.
	supergraphSDL, err := r.compose(ctx, schemas.Items)
	if err != nil {
		logger.Error(err, "supergraph composition failed")
		r.updateAllStatuses(ctx, schemas.Items, vahallav1alpha1.CompositionStatusFailed, "", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(supergraphSDL)))
	logger.Info("composition succeeded", "checksum", checksum, "subgraphs", len(schemas.Items))

	// 3. Create or update the supergraph ConfigMap.
	if err := r.upsertConfigMap(ctx, req.Namespace, supergraphSDL, checksum); err != nil {
		logger.Error(err, "failed to update supergraph ConfigMap")
		return ctrl.Result{}, err
	}

	// 4. Patch the router Deployment to trigger a rolling restart.
	if err := r.patchDeployment(ctx, req.Namespace, checksum); err != nil {
		logger.Error(err, "failed to patch router Deployment")
		return ctrl.Result{}, err
	}

	// 5. Update status on all SubgraphSchema CRs.
	r.updateAllStatuses(ctx, schemas.Items, vahallav1alpha1.CompositionStatusSuccess, checksum, "Composition succeeded")

	return ctrl.Result{}, nil
}

// compose writes schemas to a temp directory, generates a rover config, and runs rover supergraph compose.
func (r *SubgraphSchemaReconciler) compose(ctx context.Context, schemas []vahallav1alpha1.SubgraphSchema) (string, error) {
	logger := log.FromContext(ctx)

	tmpDir, err := os.MkdirTemp("", "supergraph-compose-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Sort for deterministic output.
	sort.Slice(schemas, func(i, j int) bool {
		return schemas[i].Name < schemas[j].Name
	})

	// Write each subgraph schema to a file and build the config.
	var configLines []string
	fedVersion := r.FederationVersion
	if fedVersion == "" {
		fedVersion = "=2.7.0"
	}
	configLines = append(configLines, fmt.Sprintf("federation_version: %s", fedVersion))
	configLines = append(configLines, "subgraphs:")

	for _, s := range schemas {
		schemaFile := filepath.Join(tmpDir, s.Name+".graphqls")
		if err := os.WriteFile(schemaFile, []byte(s.Spec.Schema), 0644); err != nil {
			return "", fmt.Errorf("writing schema for %s: %w", s.Name, err)
		}
		configLines = append(configLines, fmt.Sprintf("  %s:", s.Name))
		configLines = append(configLines, fmt.Sprintf("    routing_url: %s", s.Spec.RoutingUrl))
		configLines = append(configLines, "    schema:")
		configLines = append(configLines, fmt.Sprintf("      file: %s", schemaFile))
	}

	configFile := filepath.Join(tmpDir, "supergraph.yaml")
	if err := os.WriteFile(configFile, []byte(strings.Join(configLines, "\n")+"\n"), 0644); err != nil {
		return "", fmt.Errorf("writing supergraph config: %w", err)
	}

	outputFile := filepath.Join(tmpDir, "supergraph.graphql")

	roverBin := r.RoverPath
	if roverBin == "" {
		roverBin = "rover"
	}

	cmd := exec.CommandContext(ctx, roverBin,
		"supergraph", "compose",
		"--config", configFile,
		"--elv2-license", "accept",
		"--output", outputFile,
	)
	cmd.Env = append(os.Environ(), "APOLLO_TELEMETRY_DISABLED=true")

	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error(err, "rover compose failed", "output", string(output))
		return "", fmt.Errorf("rover compose: %s: %w", string(output), err)
	}

	result, err := os.ReadFile(outputFile)
	if err != nil {
		return "", fmt.Errorf("reading composed supergraph: %w", err)
	}

	return string(result), nil
}

// upsertConfigMap creates or updates the supergraph ConfigMap.
func (r *SubgraphSchemaReconciler) upsertConfigMap(ctx context.Context, namespace, supergraphSDL, checksum string) error {
	cmName := r.SupergraphConfigMap
	if cmName == "" {
		cmName = "graph-supergraph"
	}

	var cm corev1.ConfigMap
	key := types.NamespacedName{Name: cmName, Namespace: namespace}

	if err := r.Get(ctx, key, &cm); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		// Create the ConfigMap.
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "graph-controller",
					"app.kubernetes.io/part-of":    "vahalla",
				},
				Annotations: map[string]string{
					"vahalla.io/supergraph-checksum": checksum,
				},
			},
			Data: map[string]string{
				"supergraph.graphql": supergraphSDL,
			},
		}
		return r.Create(ctx, &cm)
	}

	// Update existing ConfigMap.
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations["vahalla.io/supergraph-checksum"] = checksum
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["supergraph.graphql"] = supergraphSDL
	return r.Update(ctx, &cm)
}

// patchDeployment patches the router Deployment annotation to trigger a rolling restart.
func (r *SubgraphSchemaReconciler) patchDeployment(ctx context.Context, namespace, checksum string) error {
	deployName := r.RouterDeployment
	if deployName == "" {
		deployName = "graph-router"
	}

	var deploy appsv1.Deployment
	key := types.NamespacedName{Name: deployName, Namespace: namespace}
	if err := r.Get(ctx, key, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			log.FromContext(ctx).Info("router Deployment not found, skipping patch", "name", deployName)
			return nil
		}
		return err
	}

	// Only patch if the checksum actually changed.
	currentChecksum := ""
	if deploy.Spec.Template.Annotations != nil {
		currentChecksum = deploy.Spec.Template.Annotations["vahalla.io/supergraph-checksum"]
	}
	if currentChecksum == checksum {
		return nil
	}

	patch := client.MergeFrom(deploy.DeepCopy())
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = map[string]string{}
	}
	deploy.Spec.Template.Annotations["vahalla.io/supergraph-checksum"] = checksum
	return r.Patch(ctx, &deploy, patch)
}

// updateAllStatuses updates the status of all SubgraphSchema CRs.
func (r *SubgraphSchemaReconciler) updateAllStatuses(
	ctx context.Context,
	schemas []vahallav1alpha1.SubgraphSchema,
	status vahallav1alpha1.CompositionStatus,
	checksum string,
	message string,
) {
	logger := log.FromContext(ctx)
	now := metav1.NewTime(time.Now())

	for i := range schemas {
		schemas[i].Status.CompositionStatus = status
		schemas[i].Status.Message = message
		schemas[i].Status.SupergraphChecksum = checksum
		if status == vahallav1alpha1.CompositionStatusSuccess {
			schemas[i].Status.LastComposed = &now
		}
		if err := r.Status().Update(ctx, &schemas[i]); err != nil {
			logger.Error(err, "failed to update status", "subgraph", schemas[i].Name)
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *SubgraphSchemaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vahallav1alpha1.SubgraphSchema{}).
		Complete(r)
}
