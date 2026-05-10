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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	codefiniov1alpha1 "github.com/codefin/supergraph-operator/api/v1alpha1"
	operatormetrics "github.com/codefin/supergraph-operator/internal/metrics"
)

// SubgraphSchemaReconciler reconciles SubgraphSchema objects.
type SubgraphSchemaReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	FederationVersion   string
	RouterDeployment    string
	SupergraphConfigMap string
	RoverPath           string
	CompositionTimeout  time.Duration
	DryRun              bool
	HistoryCount        int
}

// +kubebuilder:rbac:groups=codefin.io,resources=subgraphschemas,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=codefin.io,resources=subgraphschemas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

func (r *SubgraphSchemaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling SubgraphSchema", "name", req.NamespacedName)

	// 1. List all SubgraphSchema CRs in the namespace.
	var schemas codefiniov1alpha1.SubgraphSchemaList
	if err := r.List(ctx, &schemas, client.InNamespace(req.Namespace)); err != nil {
		logger.Error(err, "unable to list SubgraphSchemas")
		return ctrl.Result{}, err
	}

	if len(schemas.Items) == 0 {
		logger.Info("no SubgraphSchemas found, skipping composition")
		return ctrl.Result{}, nil
	}

	// 2. Resolve all schemas (inline or from ConfigMap).
	resolvedSDLs := make(map[string]string, len(schemas.Items))
	for _, s := range schemas.Items {
		sdl, err := r.resolveSchema(ctx, s)
		if err != nil {
			logger.Error(err, "failed to resolve schema", "subgraph", s.Name)
			r.emitEvent(schemas.Items, corev1.EventTypeWarning, "CompositionFailed", err.Error())
			r.updateAllStatuses(ctx, schemas.Items, codefiniov1alpha1.CompositionStatusFailed, "", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		resolvedSDLs[s.Name] = sdl
	}

	// 3. Schema diff detection — skip composition if schemas haven't changed.
	schemasChecksum := r.computeSchemasChecksumFromResolved(schemas.Items, resolvedSDLs)
	operatormetrics.SubgraphsTotal.Set(float64(len(schemas.Items)))
	if r.schemasUnchanged(ctx, req.Namespace, schemasChecksum) {
		logger.Info("schemas unchanged, skipping composition", "schemasChecksum", schemasChecksum)
		operatormetrics.CompositionsSkipped.Inc()
		return ctrl.Result{}, nil
	}

	// 4. Compose the supergraph.
	composeStart := time.Now()
	supergraphSDL, err := r.composeFromResolved(ctx, schemas.Items, resolvedSDLs)
	composeDuration := time.Since(composeStart)
	operatormetrics.CompositionDuration.Observe(composeDuration.Seconds())
	if err != nil {
		logger.Error(err, "supergraph composition failed")
		operatormetrics.CompositionsTotal.WithLabelValues("failed").Inc()
		r.emitEvent(schemas.Items, corev1.EventTypeWarning, "CompositionFailed", err.Error())
		r.updateAllStatuses(ctx, schemas.Items, codefiniov1alpha1.CompositionStatusFailed, "", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	operatormetrics.CompositionsTotal.WithLabelValues("success").Inc()
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(supergraphSDL)))
	logger.Info("composition succeeded", "checksum", checksum, "subgraphs", len(schemas.Items), "duration", composeDuration)

	// 5. Dry-run mode — log result but don't apply.
	if r.DryRun {
		logger.Info("dry-run mode: skipping ConfigMap/Deployment update", "checksum", checksum)
		r.emitEvent(schemas.Items, corev1.EventTypeNormal, "CompositionDryRun", fmt.Sprintf("Composed supergraph (checksum: %s), dry-run — not applied", checksum[:12]))
		return ctrl.Result{}, nil
	}

	// 6. Create or update the supergraph ConfigMap.
	if err := r.upsertConfigMap(ctx, req.Namespace, supergraphSDL, checksum, schemasChecksum); err != nil {
		logger.Error(err, "failed to update supergraph ConfigMap")
		return ctrl.Result{}, err
	}

	// 7. Patch the router Deployment to trigger a rolling restart.
	if err := r.patchDeployment(ctx, req.Namespace, checksum); err != nil {
		logger.Error(err, "failed to patch router Deployment")
		return ctrl.Result{}, err
	}

	// 8. Update status on all SubgraphSchema CRs and emit success event.
	r.emitEvent(schemas.Items, corev1.EventTypeNormal, "CompositionSucceeded", fmt.Sprintf("Composed supergraph with %d subgraphs (checksum: %s)", len(schemas.Items), checksum[:12]))
	r.updateAllStatuses(ctx, schemas.Items, codefiniov1alpha1.CompositionStatusSuccess, checksum, "Composition succeeded")

	return ctrl.Result{}, nil
}

// composeFromResolved writes pre-resolved schemas to a temp dir and runs rover supergraph compose.
func (r *SubgraphSchemaReconciler) composeFromResolved(ctx context.Context, schemas []codefiniov1alpha1.SubgraphSchema, resolvedSDLs map[string]string) (string, error) {
	logger := log.FromContext(ctx)

	// Apply composition timeout.
	timeout := r.CompositionTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
		fedVersion = "=2.13.0"
	}
	configLines = append(configLines, fmt.Sprintf("federation_version: %s", fedVersion))
	configLines = append(configLines, "subgraphs:")

	for _, s := range schemas {
		sdl := resolvedSDLs[s.Name]
		schemaFile := filepath.Join(tmpDir, s.Name+".graphqls")
		if err := os.WriteFile(schemaFile, []byte(sdl), 0644); err != nil {
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

// upsertConfigMap creates or updates the supergraph ConfigMap with optional history.
func (r *SubgraphSchemaReconciler) upsertConfigMap(ctx context.Context, namespace, supergraphSDL, checksum, schemasChecksum string) error {
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
					"app.kubernetes.io/managed-by": "supergraph-operator",
					"app.kubernetes.io/part-of":    "supergraph-operator",
				},
				Annotations: map[string]string{
					"codefin.io/supergraph-checksum": checksum,
					"codefin.io/schemas-checksum":    schemasChecksum,
				},
			},
			Data: map[string]string{
				"supergraph.graphql": supergraphSDL,
			},
		}
		return r.Create(ctx, &cm)
	}

	// Save history before overwriting.
	if r.HistoryCount > 0 {
		r.rotateHistory(&cm)
	}

	// Update existing ConfigMap.
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations["codefin.io/supergraph-checksum"] = checksum
	cm.Annotations["codefin.io/schemas-checksum"] = schemasChecksum
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["supergraph.graphql"] = supergraphSDL
	return r.Update(ctx, &cm)
}

// rotateHistory shifts previous supergraph versions in ConfigMap data keys.
func (r *SubgraphSchemaReconciler) rotateHistory(cm *corev1.ConfigMap) {
	if cm.Data == nil {
		return
	}
	// Shift existing history entries down: prev-2 = prev-1, prev-1 = current.
	for i := r.HistoryCount; i > 1; i-- {
		prevKey := fmt.Sprintf("supergraph.graphql.prev-%d", i-1)
		currKey := fmt.Sprintf("supergraph.graphql.prev-%d", i-2)
		if val, ok := cm.Data[currKey]; ok {
			cm.Data[prevKey] = val
		}
	}
	// Save current as prev-0 (most recent previous).
	if current, ok := cm.Data["supergraph.graphql"]; ok {
		cm.Data["supergraph.graphql.prev-0"] = current
	}
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
		currentChecksum = deploy.Spec.Template.Annotations["codefin.io/supergraph-checksum"]
	}
	if currentChecksum == checksum {
		return nil
	}

	patch := client.MergeFrom(deploy.DeepCopy())
	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = map[string]string{}
	}
	deploy.Spec.Template.Annotations["codefin.io/supergraph-checksum"] = checksum
	return r.Patch(ctx, &deploy, patch)
}

// updateAllStatuses updates the status of all SubgraphSchema CRs.
func (r *SubgraphSchemaReconciler) updateAllStatuses(
	ctx context.Context,
	schemas []codefiniov1alpha1.SubgraphSchema,
	status codefiniov1alpha1.CompositionStatus,
	checksum string,
	message string,
) {
	logger := log.FromContext(ctx)
	now := metav1.NewTime(time.Now())

	for i := range schemas {
		schemas[i].Status.CompositionStatus = status
		schemas[i].Status.Message = message
		schemas[i].Status.SupergraphChecksum = checksum
		if status == codefiniov1alpha1.CompositionStatusSuccess {
			schemas[i].Status.LastComposed = &now
		}
		if err := r.Status().Update(ctx, &schemas[i]); err != nil {
			logger.Error(err, "failed to update status", "subgraph", schemas[i].Name)
		}
	}
}

// resolveSchema returns the SDL for a single SubgraphSchema, loading from ConfigMapRef if set.
func (r *SubgraphSchemaReconciler) resolveSchema(ctx context.Context, s codefiniov1alpha1.SubgraphSchema) (string, error) {
	if s.Spec.SchemaFrom != nil && s.Spec.SchemaFrom.ConfigMapRef != nil {
		ref := s.Spec.SchemaFrom.ConfigMapRef
		var cm corev1.ConfigMap
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: s.Namespace}, &cm); err != nil {
			return "", fmt.Errorf("getting ConfigMap %s: %w", ref.Name, err)
		}
		sdl, ok := cm.Data[ref.Key]
		if !ok {
			return "", fmt.Errorf("key %q not found in ConfigMap %s", ref.Key, ref.Name)
		}
		return sdl, nil
	}
	if s.Spec.Schema == "" {
		return "", fmt.Errorf("either spec.schema or spec.schemaFrom.configMapRef must be set")
	}
	return s.Spec.Schema, nil
}

// computeSchemasChecksumFromResolved returns a deterministic SHA-256 using actual resolved SDL content.
func (r *SubgraphSchemaReconciler) computeSchemasChecksumFromResolved(schemas []codefiniov1alpha1.SubgraphSchema, resolvedSDLs map[string]string) string {
	sorted := make([]codefiniov1alpha1.SubgraphSchema, len(schemas))
	copy(sorted, schemas)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	h := sha256.New()
	for _, s := range sorted {
		h.Write([]byte(s.Name))
		h.Write([]byte(s.Spec.RoutingUrl))
		h.Write([]byte(resolvedSDLs[s.Name]))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// schemasUnchanged checks if the combined schemas checksum matches the stored annotation.
func (r *SubgraphSchemaReconciler) schemasUnchanged(ctx context.Context, namespace, schemasChecksum string) bool {
	cmName := r.SupergraphConfigMap
	if cmName == "" {
		cmName = "graph-supergraph"
	}

	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, &cm); err != nil {
		return false
	}
	if cm.Annotations == nil {
		return false
	}
	return cm.Annotations["codefin.io/schemas-checksum"] == schemasChecksum
}

// emitEvent records a Kubernetes Event on each SubgraphSchema CR.
func (r *SubgraphSchemaReconciler) emitEvent(schemas []codefiniov1alpha1.SubgraphSchema, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	for i := range schemas {
		r.Recorder.Event(&schemas[i], eventType, reason, message)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *SubgraphSchemaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&codefiniov1alpha1.SubgraphSchema{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.configMapToSubgraphSchemas)).
		Complete(r)
}

// configMapToSubgraphSchemas maps a ConfigMap change to all SubgraphSchemas that reference it.
func (r *SubgraphSchemaReconciler) configMapToSubgraphSchemas(ctx context.Context, obj client.Object) []reconcile.Request {
	cm := obj.(*corev1.ConfigMap)
	var schemas codefiniov1alpha1.SubgraphSchemaList
	if err := r.List(ctx, &schemas, client.InNamespace(cm.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, s := range schemas.Items {
		if s.Spec.SchemaFrom != nil && s.Spec.SchemaFrom.ConfigMapRef != nil {
			if s.Spec.SchemaFrom.ConfigMapRef.Name == cm.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: s.Name, Namespace: s.Namespace},
				})
			}
		}
	}
	return requests
}
