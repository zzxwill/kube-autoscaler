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
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/util/homedir"

	kedatype "github.com/kedacore/keda/api/v1alpha1"

	kedav1alpha1 "github.com/kedacore/keda/pkg/generated/clientset/versioned/typed/keda/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/oam-kubernetes-runtime/pkg/oam/util"
	"github.com/zzxwill/oam-autoscaler-trait/api/v1alpha1"
)

// AutoscalerReconciler reconciles a Autoscaler object
type AutoscalerReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=standard.oam.dev,resources=autoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=standard.oam.dev,resources=autoscalers/status,verbs=get;update;patch

func (r *AutoscalerReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("autoscaler", req.NamespacedName)

	// your logic here
	var scaler v1alpha1.Autoscaler
	if err := r.Get(ctx, req.NamespacedName, &scaler); err != nil {
		log.Error(err, "Could not find Autoscaler resource")
	}
	minReplicas := scaler.Spec.MinReplicas
	maxReplicas := scaler.Spec.MaxReplicas
	triggers := scaler.Spec.Triggers
	name := scaler.Name

	var kubeConfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeConfig = flag.String("kubeConfig", filepath.Join(home, ".kube", "config"), "kubeConfig file")
	}
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeConfig)
	if err != nil {
		log.Error(err, "failed to build config", "kubeConfig", kubeConfig)
		return util.ReconcileWaitResult, err
	}

	kedaClient, err := kedav1alpha1.NewForConfig(config)
	if err != nil {
		log.Error(err, "failed to initiate a KEDA client", "config", config)
		return util.ReconcileWaitResult, err
	}
	ctx = context.TODO()

	scaleTarget := kedatype.ScaleTarget{
		Name: "poc",
	}

	var kedaTriggers []kedatype.ScaleTriggers
	for _, t := range triggers {
		if t.Type == v1alpha1.CronType && t.Enabled == true {
			triggerCondition := t.Condition.CronTypeCondition

			startAt := triggerCondition.StartAt
			duration := triggerCondition.Duration
			var err error
			_, err = time.Parse("08:15", startAt)
			if err != nil {
				log.Error(err, "startAt is not in the right format, like `12:01`", "startAt", startAt)
				return util.ReconcileWaitResult, err
			}
			splitTime := strings.Split(startAt, ":")
			var startHour, startMinute, durationHour int
			if startHour, err = strconv.Atoi(splitTime[0]); err != nil {
				log.Error(err, "failed to convert hour of startAT to int")
				return util.ReconcileWaitResult, err
			}
			if startMinute, err = strconv.Atoi(splitTime[1]); err != nil {
				log.Error(err, "failed to convert minute of startAT to int")
				return util.ReconcileWaitResult, err
			}
			if !strings.HasSuffix(duration, "h") {
				log.Error(err, "currently only hours of duration is supported.", "duration", duration)
				return util.ReconcileWaitResult, err
			}

			splitDuration := strings.Split(duration, "h")
			if len(splitDuration) != 2 {
				log.Error(err, "duration hour is not in the right format, like `12h`.", "duration", duration)
				return util.ReconcileWaitResult, err
			}
			if durationHour, err = strconv.Atoi(splitDuration[0]); err != nil {
				log.Error(err, "duration hour is not in the right format, like `12h`.", "duration", duration)
				return util.ReconcileWaitResult, err
			}

			endHour := durationHour + startHour
			if endHour >= 24 {
				log.Error(err, "the sum of the hour of startAt and duration hour has to be less than 24 hours.", "startAt", startAt, "duration", duration)
				return util.ReconcileWaitResult, err
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
				kedaTrigger := kedatype.ScaleTriggers{
					Type: "cron",
					Name: name,
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
	scaleObj := kedatype.ScaledObject{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ScaledObject",
			APIVersion: "keda.k8s.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: req.NamespacedName.Namespace,
		},
		Spec: kedatype.ScaledObjectSpec{
			ScaleTargetRef:  &scaleTarget,
			MinReplicaCount: minReplicas,
			MaxReplicaCount: maxReplicas,
			Triggers:        kedaTriggers,
		},
	}
	if obj, err := kedaClient.ScaledObjects("default").Create(ctx, &scaleObj, metav1.CreateOptions{}); err != nil {
		log.Error(err, "failed to create KEDA ScaledObj", "ScaledObject", obj)
		return util.ReconcileWaitResult, err
	}

	return ctrl.Result{}, nil
}

func (r *AutoscalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Autoscaler{}).
		Complete(r)
}
