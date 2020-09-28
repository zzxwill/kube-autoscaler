/*


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
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/util/homedir"

	kedav1alpha1 "github.com/kedacore/keda/api/v1alpha1"

	kedatype "github.com/kedacore/keda/pkg/generated/clientset/versioned/typed/keda/v1alpha1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zzxwill/oam-autoscaler-trait/api/v1alpha1"
	restclient "k8s.io/client-go/rest"
)

const (
	SpecWarningTargetWorkloadNotSet = "Spec.targetWorkload is not set"
	SpecWarningStartAtTimeFormat    = "startAt is not in the right format, which should be like `12:01`"
)

var (
	scaledObjectKind       = reflect.TypeOf(kedav1alpha1.ScaledObject{}).Name()
	scaledObjectAPIVersion = "keda.k8s.io/v1alpha1"
)

// ReconcileWaitResult is the time to wait between reconciliation.
var ReconcileWaitResult = reconcile.Result{RequeueAfter: 30 * time.Second}

// AutoscalerReconciler reconciles a Autoscaler object
type AutoscalerReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
	record event.Recorder
	config *restclient.Config
	ctx    context.Context
}

// +kubebuilder:rbac:groups=standard.oam.dev,resources=autoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=standard.oam.dev,resources=autoscalers/status,verbs=get;update;patch
func (r *AutoscalerReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("autoscaler", req.NamespacedName)
	log.Info("Reconciling Autoscaler...")

	var scaler v1alpha1.Autoscaler
	if err := r.Get(r.ctx, req.NamespacedName, &scaler); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Autoscaler is deleted")
		}
		return ReconcileWaitResult, client.IgnoreNotFound(err)
	}
	log.Info("retrieved trait Autoscaler", "APIVersion", scaler.APIVersion, "Kind", scaler.Kind)

	// find ApplicationConfiguration to record the event
	// comment it as I don't want Autoscaler to know it's in OAM context
	//eventObj, err := util.LocateParentAppConfig(ctx, r.Client, &scaler)
	//if err != nil {
	//	log.Error(err, "failed to locate ApplicationConfiguration", "AutoScaler", scaler.Name)
	//}

	namespace := req.NamespacedName.Namespace
	return r.scaleByKEDA(scaler, namespace, log)
}

func (r *AutoscalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.buildConfig(); err != nil {
		return err
	}
	r.ctx = context.Background()
	r.record = event.NewAPIRecorder(mgr.GetEventRecorderFor("Autoscaler")).
		WithAnnotations("controller", "Autoscaler")
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Autoscaler{}).
		Complete(r)
}

func (r *AutoscalerReconciler) scaleByKEDA(scaler v1alpha1.Autoscaler, namespace string, log logr.Logger) (ctrl.Result, error) {
	config := r.config
	kedaClient, err := kedatype.NewForConfig(config)
	if err != nil {
		log.Error(err, "failed to initiate a KEDA client", "config", config)
		return ReconcileWaitResult, err
	}

	minReplicas := scaler.Spec.MinReplicas
	maxReplicas := scaler.Spec.MaxReplicas
	triggers := scaler.Spec.Triggers
	scalerName := scaler.Name
	targetWorkload := scaler.Spec.TargetWorkload

	var kedaTriggers []kedav1alpha1.ScaleTriggers
	for _, t := range triggers {
		if t.Type == v1alpha1.CronType {
			if targetWorkload.Name == "" {
				err := errors.New(SpecWarningTargetWorkloadNotSet)
				log.Error(err, "")
				r.record.Event(&scaler, event.Warning(SpecWarningTargetWorkloadNotSet, err))
				return ReconcileWaitResult, err
			}

			triggerCondition := t.Condition.CronTypeCondition
			startAt := triggerCondition.StartAt
			duration := triggerCondition.Duration
			var err error
			startTime, err := time.Parse("15:04", startAt)
			if err != nil {
				log.Error(err, SpecWarningStartAtTimeFormat, startAt)
				r.record.Event(&scaler, event.Warning(SpecWarningStartAtTimeFormat, err))
				return ReconcileWaitResult, err
			}
			var startHour, startMinute, durationHour int
			startHour = startTime.Hour()
			startMinute = startTime.Minute()
			if !strings.HasSuffix(duration, "h") {
				log.Error(err, "currently only hours of duration is supported.", "duration", duration)
				return ReconcileWaitResult, err
			}

			splitDuration := strings.Split(duration, "h")
			if len(splitDuration) != 2 {
				log.Error(err, "duration hour is not in the right format, like `12h`.", "duration", duration)
				return ReconcileWaitResult, err
			}
			if durationHour, err = strconv.Atoi(splitDuration[0]); err != nil {
				log.Error(err, "duration hour is not in the right format, like `12h`.", "duration", duration)
				return ReconcileWaitResult, err
			}

			endHour := durationHour + startHour
			if endHour >= 24 {
				log.Error(err, "the sum of the hour of startAt and duration hour has to be less than 24 hours.", "startAt", startAt, "duration", duration)
				return ReconcileWaitResult, err
			}
			replicas := triggerCondition.Replicas

			timezone := triggerCondition.Timezone

			days := triggerCondition.Days
			var dayNo []int

			var i = 0

			// TODO(@zzxwill) On Mac, it's Sunday when i == 0, need check on Linux
			for _, d := range days {
				for i < 7 {
					if strings.EqualFold(time.Weekday(i).String(), d) {
						dayNo = append(dayNo, i)
						break
					}
					i += 1
				}
			}

			for _, n := range dayNo {
				kedaTrigger := kedav1alpha1.ScaleTriggers{
					Type: string(t.Type),
					Name: t.Name,
					Metadata: map[string]string{
						"timezone":        timezone,
						"start":           fmt.Sprintf("%d %d * * %d", startMinute, startHour, n),
						"end":             fmt.Sprintf("%d %d * * %d", startMinute, endHour, n),
						"desiredReplicas": strconv.Itoa(replicas),
					},
				}
				kedaTriggers = append(kedaTriggers, kedaTrigger)
			}
		}
	}

	scaleTarget := kedav1alpha1.ScaleTarget{
		Name: targetWorkload.Name,
	}

	scaleObj := kedav1alpha1.ScaledObject{
		TypeMeta: metav1.TypeMeta{
			Kind:       scaledObjectKind,
			APIVersion: scaledObjectAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      scalerName,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         scaler.APIVersion,
					Kind:               scaler.Kind,
					UID:                scaler.GetUID(),
					Name:               scalerName,
					Controller:         pointer.BoolPtr(true),
					BlockOwnerDeletion: pointer.BoolPtr(true),
				},
			},
		},
		Spec: kedav1alpha1.ScaledObjectSpec{
			ScaleTargetRef:  &scaleTarget,
			MinReplicaCount: minReplicas,
			MaxReplicaCount: maxReplicas,
			Triggers:        kedaTriggers,
		},
	}
	if obj, err := kedaClient.ScaledObjects("default").Create(r.ctx, &scaleObj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		log.Error(err, "failed to create KEDA ScaledObj", "ScaledObject", obj)
		return ReconcileWaitResult, err
	}
	return ctrl.Result{}, nil
}

func (r *AutoscalerReconciler) buildConfig() error {
	var kubeConfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeConfig = flag.String("kubeConfig", filepath.Join(home, ".kube", "config"), "kubeConfig file")
	}
	flag.Parse()
	config, err := clientcmd.BuildConfigFromFlags("", *kubeConfig)
	if err != nil {
		return err
	}
	r.config = config
	return nil
}
