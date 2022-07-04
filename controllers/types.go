package controllers

import "github.com/zzxwill/kube-autoscaler/api/v1alpha1"

const (
	CPUType              v1alpha1.TriggerType = "cpu"
	MemoryType           v1alpha1.TriggerType = "memory"
	StorageType          v1alpha1.TriggerType = "storage"
	EphemeralStorageType v1alpha1.TriggerType = "ephemeral-storage"
	CronType             v1alpha1.TriggerType = "cron"
)
