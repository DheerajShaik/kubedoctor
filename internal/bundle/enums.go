package bundle

// These are the structured Kubernetes classifications that the collector may
// retain. Arbitrary reasons supplied by an imported artifact stay outside the
// analyzer even when the artifact has a recomputed integrity hash.
func validEventReason(reason string) bool {
	return oneOf(reason, "FailedScheduling", "FailedMount", "FailedAttachVolume", "FailedBinding", "ProvisioningFailed",
		"FailedCreate", "FailedDelete", "Failed", "BackOff", "Unhealthy", "Evicted", "NodeNotReady",
		"FailedCreatePodSandBox", "FailedToRetrieveImagePullSecret", "FailedMapVolume", "MissSchedule", "TooManyMissedTimes",
		"UnknownTimeZone", "WaitForFirstConsumer", "WaitForPodScheduled", "JobAlreadyActive")
}

func validConditionType(kind string) bool {
	return oneOf(kind, "Ready", "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable",
		"PodScheduled", "ContainersReady", "Initialized", "PodReadyToStartContainers",
		"Available", "Progressing", "ReplicaFailure", "DisruptionAllowed")
}

func validConditionReason(reason string) bool {
	return oneOf(reason, "", "KubeletReady", "KubeletNotReady", "NodeStatusUnknown", "NodeStatusNeverUpdated",
		"KubeletHasSufficientMemory", "KubeletHasInsufficientMemory", "KubeletHasNoDiskPressure", "KubeletHasDiskPressure",
		"KubeletHasSufficientPID", "KubeletHasInsufficientPID", "RouteCreated", "NoRouteCreated",
		"Unschedulable", "SchedulingGated", "ContainersNotReady", "PodCompleted",
		"ProgressDeadlineExceeded", "MinimumReplicasAvailable", "MinimumReplicasUnavailable", "NewReplicaSetAvailable",
		"FoundNewReplicaSet", "NewReplicaSetCreated", "ReplicaSetUpdated", "DeploymentPaused", "DeploymentResumed",
		"FailedCreate", "FailedDelete", "SyncFailed", "InsufficientPods", "SufficientPods")
}

func validPodReason(reason string) bool {
	return oneOf(reason, "", "Evicted", "NodeLost", "DeadlineExceeded", "Shutdown", "ContainerStatusUnknown", "UnexpectedAdmissionError")
}

func validWaitingReason(reason string) bool {
	return oneOf(reason, "", "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "ErrImageNeverPull", "InvalidImageName",
		"RegistryUnavailable", "ImageInspectError", "CreateContainerConfigError", "CreateContainerError",
		"RunContainerError", "StartError", "ContainerCreating", "PodInitializing")
}

func validTerminationReason(reason string) bool {
	return oneOf(reason, "", "OOMKilled", "Error", "Completed", "ContainerCannotRun", "DeadlineExceeded", "ContainerStatusUnknown", "StartError")
}

func validBriefConditionType(kind string) bool {
	return oneOf(kind, "Complete", "Failed", "FailureTarget", "SuccessCriteriaMet", "Suspended",
		"AbleToScale", "ScalingActive", "ScalingLimited")
}

func validBriefConditionReason(reason string) bool {
	return oneOf(reason, "", "BackoffLimitExceeded", "DeadlineExceeded", "PodFailurePolicy", "FailedIndexes", "MaxFailedIndexesExceeded",
		"SuccessPolicy", "CompletionsReached", "JobSuspended", "JobResumed", "FailedGetScale", "FailedUpdateScale",
		"FailedGetResourceMetric", "FailedGetContainerResourceMetric", "FailedGetPodsMetric", "FailedGetObjectMetric",
		"FailedGetExternalMetric", "FailedComputeMetricsReplicas", "InvalidMetricSourceType", "ScalingDisabled", "ReadyForNewScale",
		"SucceededGetScale", "ValidMetricFound", "DesiredWithinRange", "TooManyReplicas", "TooFewReplicas", "ScaleUpLimit",
		"ScaleDownLimit", "ScaleDownStabilized", "ScaleUpStabilized")
}
