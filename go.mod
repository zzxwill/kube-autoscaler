module github.com/zzxwill/oam-autoscaler-trait

go 1.13

require (
	github.com/crossplane/crossplane-runtime v0.8.0
	github.com/go-logr/logr v0.1.0 // use the version due to issue https://github.com/kubernetes-sigs/controller-runtime/issues/1033
	github.com/kedacore/keda v1.5.1
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	k8s.io/api v0.18.8
	k8s.io/apimachinery v0.18.8
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/utils v0.0.0-20200603063816-c1c6865ac451
	sigs.k8s.io/controller-runtime v0.6.3 // due to issue https://github.com/kubernetes-sigs/controller-runtime/issues/1033
)

replace k8s.io/client-go => k8s.io/client-go v0.18.8

replace github.com/kedacore/keda => github.com/zzxwill/keda v1.5.1
