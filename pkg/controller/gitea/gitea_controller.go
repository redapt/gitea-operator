package gitea

import (
	"context"
	"reflect"

	redaptv1alpha1 "github.com/redapt/gitea-operator/pkg/apis/redapt/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_gitea")

// Add creates a new Gitea Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileGitea{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("gitea-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource Gitea
	err = c.Watch(&source.Kind{Type: &redaptv1alpha1.Gitea{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to secondary resource Pods and requeue the owner Gitea
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &redaptv1alpha1.Gitea{},
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileGitea implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileGitea{}

// ReconcileGitea reconciles a Gitea object
type ReconcileGitea struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a Gitea object and makes changes based on the state read
// and what is in the Gitea.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileGitea) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling Gitea")

	// Fetch the Gitea instance
	gitea := &redaptv1alpha1.Gitea{}
	err := r.client.Get(context.TODO(), request.NamespacedName, gitea)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}


	// Check if this Deployment already exists
	found := &appsv1.Deployment{}
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: gitea.Name + "-deployment", Namespace: gitea.Namespace}, found)
	if err != nil && errors.IsNotFound(err) {
		reqLogger.Info("Creating a new Deployment", "Deployment.Namespace", gitea.Namespace, "Deployment.Name", gitea.Name)
		// Define a new Deployment object
		deployment := newDeploymentForCR(gitea)
		if err := controllerutil.SetControllerReference(gitea, deployment, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		err = r.client.Create(context.TODO(), deployment)
		if err != nil {
			return reconcile.Result{}, err
		}

		// Pod created successfully - don't requeue
		return reconcile.Result{Requeue: true}, nil
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Ensure the deployment size is the same as the spec
	size := gitea.Spec.Size
	if *found.Spec.Replicas != size {
		found.Spec.Replicas = &size
		err = r.client.Update(context.TODO(), found)
		if err != nil {
			reqLogger.Error(err, "Failed to update Deployment.", "Deployment.Namespace", found.Namespace, "Deployment.Name", found.Name)
			return reconcile.Result{}, err
		}
		// Spec updated - return and requeue
		return reconcile.Result{Requeue: true}, nil
	}

	// Update deployment status with the pod names
	// TODO: currently correct on initial deploy, then on edits it's 1 edit behind.  For example, deploy with size 3, edit to size 1 - it will show original 3 pods, edit back to size 3 - it will show 1 pod
	podList := &corev1.PodList{}
	labelSelector := labels.SelectorFromSet(
		map[string]string{
			"app": gitea.Name,
		})
	listOps := &client.ListOptions{
		Namespace:     gitea.Namespace,
		LabelSelector: labelSelector,
	}
	err = r.client.List(context.TODO(), listOps, podList)
	if err != nil {
		reqLogger.Error(err, "Failed to list pods.", "Gitea.Namespace", gitea.Namespace, "Gitea.Name", gitea.Name)
		return reconcile.Result{}, err
	}
	podNames := getPodNames(podList.Items)

	// Update status.Pods if needed
	if !reflect.DeepEqual(podNames, gitea.Status.Pods) {
		gitea.Status.Pods = podNames
		err := r.client.Status().Update(context.TODO(), gitea)
		if err != nil {
			reqLogger.Error(err, "Failed to update deployment status.")
			return reconcile.Result{}, err
		}
	}


	return reconcile.Result{}, nil
}

func newDeploymentForCR(cr *redaptv1alpha1.Gitea) *appsv1.Deployment {
	deploymentLabels := map[string]string{
		"app": cr.Name,
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name + "-deployment",
			Namespace: cr.Namespace,
			Labels:    deploymentLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &cr.Spec.Size,
			Selector: &metav1.LabelSelector{
				MatchLabels: deploymentLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cr.Name + "-pod",
					Namespace: cr.Namespace,
					Labels:    deploymentLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "gitea",
							Image: "gitea/gitea:latest",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 3000,
									Name: "http-port",
								},
								{
									ContainerPort: 22,
									HostPort: 2222,
									Name: "ssh-port",
								},
							},
						},
					},
				},
			},
			Strategy:                appsv1.DeploymentStrategy{},
			MinReadySeconds:         0,
			RevisionHistoryLimit:    nil,
			Paused:                  false,
			ProgressDeadlineSeconds: nil,
		},
	}
}

// getPodNames returns the pod names of the array of pods passed in
func getPodNames(pods []corev1.Pod) []string {
	var podNames []string
	for _, pod := range pods {
		podNames = append(podNames, pod.Name)
	}
	return podNames
}
