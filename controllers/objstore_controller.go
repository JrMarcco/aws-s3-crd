/*
Copyright 2022.

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

package controllers

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cninfv1alpha1 "aws-s3-crd/api/v1alpha1"
)

const (
	configMapName = "%s-cm"
	finalizer     = "objstores.cninf.jrmarcco.io/finalizer"
)

// ObjStoreReconciler reconciles a ObjStore object
type ObjStoreReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	S3svc  *s3.S3
}

//+kubebuilder:rbac:groups=cninf.jrmarcco.io,resources=objstores,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cninf.jrmarcco.io,resources=objstores/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cninf.jrmarcco.io,resources=objstores/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=ConfigMap,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ObjStore object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *ObjStoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	inst := &cninfv1alpha1.ObjStore{}
	if err := r.Get(ctx, req.NamespacedName, inst); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if inst.ObjectMeta.DeletionTimestamp.IsZero() {
		if inst.Status.State == "" {
			inst.Status.State = cninfv1alpha1.PendingState
			// update status
			_ = r.Status().Update(ctx, inst)
		}
		controllerutil.AddFinalizer(inst, finalizer)
		if err := r.Update(ctx, inst); err != nil {
			return ctrl.Result{}, err
		}

		if inst.Status.State == cninfv1alpha1.PendingState {
			// create resources
			if err := r.CreateResources(ctx, inst); err != nil {
				inst.Status.State = cninfv1alpha1.ErrorState
				_ = r.Status().Update(ctx, inst)
				return ctrl.Result{}, err
			}
		}
	} else {
		// delete resources
		if err := r.DeleteResources(ctx, inst); err != nil {
			inst.Status.State = cninfv1alpha1.ErrorState
			_ = r.Status().Update(ctx, inst)
			return ctrl.Result{}, err
		}
		// release finalizer
		controllerutil.RemoveFinalizer(inst, finalizer)
		if err := r.Update(ctx, inst); err != nil {
			return ctrl.Result{}, nil
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ObjStoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cninfv1alpha1.ObjStore{}).
		Complete(r)
}

func (r *ObjStoreReconciler) CreateResources(ctx context.Context, objstore *cninfv1alpha1.ObjStore) error {
	// update status
	objstore.Status.State = cninfv1alpha1.CreatingState
	if err := r.Status().Update(ctx, objstore); err != nil {
		return err
	}

	// create s3 bucket
	b, err := r.S3svc.CreateBucket(&s3.CreateBucketInput{
		Bucket:                     aws.String(objstore.Spec.Name),
		ObjectLockEnabledForBucket: aws.Bool(objstore.Spec.Locked),
	})
	if err != nil {
		return err
	}

	// wait for it to be created
	if err = r.S3svc.WaitUntilBucketExists(&s3.HeadBucketInput{Bucket: aws.String(objstore.Spec.Name)}); err != nil {
		return nil
	}

	// create configmap
	data := map[string]string{
		"name":     objstore.Spec.Name,
		"location": *b.Location,
	}
	cm := &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf(configMapName, objstore.Spec.Name),
			Namespace: objstore.Namespace,
		},
		Data: data,
	}

	if err = r.Create(ctx, cm); err != nil {
		return err
	}

	// update status
	objstore.Status.State = cninfv1alpha1.CreatedState
	if err = r.Status().Update(ctx, objstore); err != nil {
		return err
	}

	return nil
}

func (r *ObjStoreReconciler) DeleteResources(ctx context.Context, objstore *cninfv1alpha1.ObjStore) error {
	// delete s3 bucket
	_, err := r.S3svc.DeleteBucket(&s3.DeleteBucketInput{Bucket: aws.String(objstore.Spec.Name)})
	if err != nil {
		return err
	}

	// delete configmap
	cm := &v1.ConfigMap{}
	err = r.Get(ctx, client.ObjectKey{
		Name:      fmt.Sprintf(configMapName, objstore.Spec.Name),
		Namespace: objstore.Namespace,
	}, cm)
	if err != nil {
		return err
	}

	if err = r.Delete(ctx, cm); err != nil {
		return err
	}
	return nil
}
