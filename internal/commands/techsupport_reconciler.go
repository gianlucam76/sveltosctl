/*
Copyright 2023. projectsveltos.io. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package commands

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2/textlogger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	libsveltosv1beta1 "github.com/projectsveltos/libsveltos/api/v1beta1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
	utilsv1beta1 "github.com/projectsveltos/sveltosctl/api/v1beta1"
	"github.com/projectsveltos/sveltosctl/internal/collector"
	"github.com/projectsveltos/sveltosctl/internal/utils"
)

func TechsupportReconciler(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(1)))
	logger.V(logs.LogInfo).Info("Reconciling")

	accessInstance := utils.GetAccessInstance()

	techsupportInstance := &utilsv1beta1.Techsupport{}
	if err := accessInstance.GetResource(ctx, req.NamespacedName, techsupportInstance); err != nil {
		logger.Error(err, "unable to fetch Techsupport")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger = logger.WithValues("techsupport", techsupportInstance.Name)

	if !techsupportInstance.DeletionTimestamp.IsZero() {
		if result, err := reconcileDelete(ctx, techsupportInstance, collector.Techsupport, techsupportInstance.Spec.Storage,
			utilsv1beta1.TechsupportFinalizer, logger); err != nil {
			return result, err
		}
		cleanMaps(techsupportInstance)
		return reconcile.Result{}, nil
	}

	return reconcileTechsupportNormal(ctx, techsupportInstance, logger)
}

func reconcileTechsupportNormal(ctx context.Context, techsupportInstance *utilsv1beta1.Techsupport,
	logger logr.Logger) (reconcile.Result, error) {

	logger.V(logs.LogInfo).Info("reconcileTechsupportNormal")
	if err := addFinalizer(ctx, techsupportInstance, utilsv1beta1.TechsupportFinalizer); err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to add finalizer: %s", err))
		return reconcile.Result{}, err
	}

	matchingClusters, err := getMatchingClusters(ctx, techsupportInstance)
	if err != nil {
		return reconcile.Result{}, err
	}

	techsupportInstance.Status.MatchingClusterRefs = matchingClusters

	updateMaps(techsupportInstance)

	collectionTechsupport := collectionTechsupport{techsupportInstance: techsupportInstance}
	techsupportClient := collector.GetClient()
	// Get result, if any, from previous run
	result := techsupportClient.GetResult(ctx, techsupportInstance.Name, collector.Techsupport)
	updateStatus(result, &collectionTechsupport)

	now := time.Now()
	nextRun, err := schedule(ctx, techsupportInstance, collector.Techsupport,
		collectTechsupport, &collectionTechsupport, logger)
	if err != nil {
		logger.V(logs.LogInfo).Info("failed to get next run. Err: %v", err)
		return ctrl.Result{}, err
	}

	techsupportInstance = collectionTechsupport.techsupportInstance

	logger.V(logs.LogInfo).Info("patching techsupport instance")
	err = utils.GetAccessInstance().UpdateResourceStatus(ctx, techsupportInstance)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to patch. Err: %v", err))
		return reconcile.Result{Requeue: true, RequeueAfter: requeueAfter}, nil
	}
	if isCollectionInProgress(techsupportInstance.Status.LastRunStatus) {
		logger.V(logs.LogInfo).Info("techsupport collection still in progress")
		return reconcile.Result{Requeue: true, RequeueAfter: requeueAfter}, nil
	}

	logger.V(logs.LogInfo).Info("reconcile techsupport succeeded")
	scheduledResult := ctrl.Result{RequeueAfter: nextRun.Sub(now)}
	return scheduledResult, nil
}

type ClusterPredicate struct {
	Logger logr.Logger
}

func (p ClusterPredicate) Create(obj event.TypedCreateEvent[*clusterv1.Cluster]) bool {
	cluster := obj.Object
	log := p.Logger.WithValues("predicate", "createEvent",
		"namespace", cluster.Namespace,
		"cluster", cluster.Name,
	)

	// Only need to trigger a reconcile if the Cluster.Spec.Paused is false
	if !cluster.Spec.Paused {
		log.V(logs.LogVerbose).Info(
			"Cluster is not paused.  Will attempt to reconcile associated ClusterProfiles.",
		)
		return true
	}
	log.V(logs.LogVerbose).Info(
		"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
	return false
}

func (p ClusterPredicate) Update(obj event.TypedUpdateEvent[*clusterv1.Cluster]) bool {
	newCluster := obj.ObjectNew
	oldCluster := obj.ObjectOld
	log := p.Logger.WithValues("predicate", "updateEvent",
		"namespace", newCluster.Namespace,
		"cluster", newCluster.Name,
	)

	if oldCluster == nil {
		log.V(logs.LogVerbose).Info("Old Cluster is nil. Reconcile ClusterProfile")
		return true
	}

	// return true if Cluster.Spec.Paused has changed from true to false
	if oldCluster.Spec.Paused && !newCluster.Spec.Paused {
		log.V(logs.LogVerbose).Info(
			"Cluster was unpaused. Will attempt to reconcile associated ClusterProfiles.")
		return true
	}

	if !reflect.DeepEqual(oldCluster.Labels, newCluster.Labels) {
		log.V(logs.LogVerbose).Info(
			"Cluster labels changed. Will attempt to reconcile associated ClusterProfiles.",
		)
		return true
	}

	// otherwise, return false
	log.V(logs.LogVerbose).Info(
		"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
	return false
}

func (p ClusterPredicate) Delete(obj event.TypedDeleteEvent[*clusterv1.Cluster]) bool {
	log := p.Logger.WithValues("predicate", "deleteEvent",
		"namespace", obj.Object.GetNamespace(),
		"cluster", obj.Object.GetName(),
	)
	log.V(logs.LogVerbose).Info(
		"Cluster deleted.  Will attempt to reconcile associated ClusterProfiles.")
	return true
}

func (p ClusterPredicate) Generic(obj event.TypedGenericEvent[*clusterv1.Cluster]) bool {
	log := p.Logger.WithValues("predicate", "genericEvent",
		"namespace", obj.Object.GetNamespace(),
		"cluster", obj.Object.GetName(),
	)
	log.V(logs.LogVerbose).Info(
		"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
	return false
}

type SveltosClusterPredicate struct {
	Logger logr.Logger
}

func (p SveltosClusterPredicate) Create(obj event.TypedCreateEvent[*libsveltosv1beta1.SveltosCluster]) bool {
	cluster := obj.Object
	log := p.Logger.WithValues("predicate", "createEvent",
		"namespace", cluster.Namespace,
		"cluster", cluster.Name,
	)

	// Only need to trigger a reconcile if the Cluster.Spec.Paused is false
	if !cluster.Spec.Paused {
		log.V(logs.LogVerbose).Info(
			"Cluster is not paused.  Will attempt to reconcile associated ClusterProfiles.",
		)
		return true
	}
	log.V(logs.LogVerbose).Info(
		"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
	return false
}

func (p SveltosClusterPredicate) Update(obj event.TypedUpdateEvent[*libsveltosv1beta1.SveltosCluster]) bool {
	newCluster := obj.ObjectNew
	oldCluster := obj.ObjectOld
	log := p.Logger.WithValues("predicate", "updateEvent",
		"namespace", newCluster.Namespace,
		"cluster", newCluster.Name,
	)

	if oldCluster == nil {
		log.V(logs.LogVerbose).Info("Old Cluster is nil. Reconcile ClusterProfile")
		return true
	}

	// return true if Cluster.Spec.Paused has changed from true to false
	if oldCluster.Spec.Paused && !newCluster.Spec.Paused {
		log.V(logs.LogVerbose).Info(
			"Cluster was unpaused. Will attempt to reconcile associated ClusterProfiles.")
		return true
	}

	if !oldCluster.Status.Ready && newCluster.Status.Ready {
		log.V(logs.LogVerbose).Info(
			"Cluster was not ready. Will attempt to reconcile associated ClusterProfiles.")
		return true
	}

	if !reflect.DeepEqual(oldCluster.Labels, newCluster.Labels) {
		log.V(logs.LogVerbose).Info(
			"Cluster labels changed. Will attempt to reconcile associated ClusterProfiles.",
		)
		return true
	}

	// otherwise, return false
	log.V(logs.LogVerbose).Info(
		"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
	return false
}

func (p SveltosClusterPredicate) Delete(obj event.TypedDeleteEvent[*libsveltosv1beta1.SveltosCluster]) bool {
	log := p.Logger.WithValues("predicate", "deleteEvent",
		"namespace", obj.Object.GetNamespace(),
		"cluster", obj.Object.GetName(),
	)
	log.V(logs.LogVerbose).Info(
		"Cluster deleted.  Will attempt to reconcile associated ClusterProfiles.")
	return true
}

func (p SveltosClusterPredicate) Generic(obj event.TypedGenericEvent[*libsveltosv1beta1.SveltosCluster]) bool {
	log := p.Logger.WithValues("predicate", "genericEvent",
		"namespace", obj.Object.GetNamespace(),
		"cluster", obj.Object.GetName(),
	)
	log.V(logs.LogVerbose).Info(
		"Cluster did not match expected conditions.  Will not attempt to reconcile associated ClusterProfiles.")
	return false
}

func updateMaps(techsupport *utilsv1beta1.Techsupport) {
	currentClusters := &libsveltosset.Set{}
	for i := range techsupport.Status.MatchingClusterRefs {
		cluster := techsupport.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name, Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		currentClusters.Insert(clusterInfo)
	}

	mux.Lock()
	defer mux.Unlock()

	techsupportInfo := getKeyFromObject(techsupport)

	// Get list of Clusters not matched anymore by Techsupport
	var toBeRemoved []corev1.ObjectReference
	if v, ok := techsupportMap[*techsupportInfo]; ok {
		toBeRemoved = v.Difference(currentClusters)
	}

	// For each currently matching Cluster, add Techsupport as consumer
	for i := range techsupport.Status.MatchingClusterRefs {
		cluster := techsupport.Status.MatchingClusterRefs[i]
		clusterInfo := &corev1.ObjectReference{Namespace: cluster.Namespace, Name: cluster.Name, Kind: cluster.Kind, APIVersion: cluster.APIVersion}
		getClusterMapForEntry(clusterInfo).Insert(techsupportInfo)
	}

	// For each Cluster not matched anymore, remove Techsupport as consumer
	for i := range toBeRemoved {
		clusterInfo := toBeRemoved[i]
		getClusterMapForEntry(&clusterInfo).Erase(techsupportInfo)
	}

	techsupportMap[*techsupportInfo] = currentClusters
	techsupports[*techsupportInfo] = techsupport.Spec.ClusterSelector
}

func cleanMaps(techsupport *utilsv1beta1.Techsupport) {
	mux.Lock()
	defer mux.Unlock()

	techsupportInfo := getKeyFromObject(techsupport)

	delete(techsupportMap, *techsupportInfo)
	delete(techsupports, *techsupportInfo)

	for i := range clusterMap {
		techsupportSet := clusterMap[i]
		techsupportSet.Erase(techsupportInfo)
	}
}

// getKeyFromObject returns the Key that can be used in the internal reconciler maps.
func getKeyFromObject(obj client.Object) *corev1.ObjectReference {
	scheme, _ := utils.GetScheme()
	addTypeInformationToObject(scheme, obj)

	return &corev1.ObjectReference{
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
		Kind:       obj.GetObjectKind().GroupVersionKind().Kind,
		APIVersion: obj.GetObjectKind().GroupVersionKind().String(),
	}
}

func addTypeInformationToObject(scheme *runtime.Scheme, obj client.Object) {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		panic(1)
	}

	for _, gvk := range gvks {
		if gvk.Kind == "" {
			continue
		}
		if gvk.Version == "" || gvk.Version == runtime.APIVersionInternal {
			continue
		}
		obj.GetObjectKind().SetGroupVersionKind(gvk)
		break
	}
}

func getClusterMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := clusterMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		clusterMap[*entry] = s
	}
	return s
}

// getMatchingClusters returns all Sveltos/CAPI Clusters currently matching Techsupport.Spec.ClusterSelector
func getMatchingClusters(ctx context.Context, techsupport *utilsv1beta1.Techsupport,
) ([]corev1.ObjectReference, error) {

	matching := make([]corev1.ObjectReference, 0)

	clusterSelector, err := metav1.LabelSelectorAsSelector(&techsupport.Spec.ClusterSelector.LabelSelector)
	if err != nil {
		return nil, err
	}

	tmpMatching, err := getMatchingCAPIClusters(ctx, clusterSelector)
	if err != nil {
		return nil, err
	}

	matching = append(matching, tmpMatching...)

	tmpMatching, err = getMatchingSveltosClusters(ctx, clusterSelector)
	if err != nil {
		return nil, err
	}

	matching = append(matching, tmpMatching...)

	return matching, nil
}

func getMatchingCAPIClusters(ctx context.Context, parsedSelector labels.Selector) ([]corev1.ObjectReference, error) {
	instance := utils.GetAccessInstance()

	clusterList := &clusterv1.ClusterList{}
	if err := instance.ListResources(ctx, clusterList); err != nil {
		return nil, err
	}

	matching := make([]corev1.ObjectReference, 0)

	for i := range clusterList.Items {
		cluster := &clusterList.Items[i]

		if !cluster.DeletionTimestamp.IsZero() {
			// Only existing cluster can match
			continue
		}

		addTypeInformationToObject(instance.GetScheme(), cluster)
		if parsedSelector.Matches(labels.Set(cluster.Labels)) {
			matching = append(matching, corev1.ObjectReference{
				Kind:       cluster.Kind,
				Namespace:  cluster.Namespace,
				Name:       cluster.Name,
				APIVersion: cluster.APIVersion,
			})
		}
	}

	return matching, nil
}

func getMatchingSveltosClusters(ctx context.Context, parsedSelector labels.Selector) ([]corev1.ObjectReference, error) {
	instance := utils.GetAccessInstance()

	clusterList := &libsveltosv1beta1.SveltosClusterList{}
	if err := instance.ListResources(ctx, clusterList); err != nil {
		return nil, err
	}

	matching := make([]corev1.ObjectReference, 0)

	for i := range clusterList.Items {
		cluster := &clusterList.Items[i]

		if !cluster.DeletionTimestamp.IsZero() {
			// Only existing cluster can match
			continue
		}

		addTypeInformationToObject(instance.GetScheme(), cluster)
		if parsedSelector.Matches(labels.Set(cluster.Labels)) {
			matching = append(matching, corev1.ObjectReference{
				Kind:       cluster.Kind,
				Namespace:  cluster.Namespace,
				Name:       cluster.Name,
				APIVersion: cluster.APIVersion,
			})
		}
	}

	return matching, nil
}
