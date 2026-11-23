package analyze

import (
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/model"
	core "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
)

type configReference struct {
	kind, name, key, container string
	volume                     bool
}

func requiredConfiguration(pod *core.Pod) []configReference {
	var refs []configReference
	add := func(ref configReference, optional *bool) {
		if ref.name != "" && (optional == nil || !*optional) {
			refs = append(refs, ref)
		}
	}
	addVolume := func(kind, name string, items []core.KeyToPath, optional *bool) {
		if len(items) == 0 {
			add(configReference{kind: kind, name: name, volume: true}, optional)
		}
		for _, item := range items {
			add(configReference{kind: kind, name: name, key: item.Key, volume: true}, optional)
		}
	}
	for _, containers := range [][]core.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, c := range containers {
			for _, env := range c.Env {
				if env.ValueFrom == nil {
					continue
				}
				if cm := env.ValueFrom.ConfigMapKeyRef; cm != nil {
					add(configReference{kind: "ConfigMap", name: cm.Name, key: cm.Key, container: c.Name}, cm.Optional)
				}
				if secret := env.ValueFrom.SecretKeyRef; secret != nil {
					add(configReference{kind: "Secret", name: secret.Name, key: secret.Key, container: c.Name}, secret.Optional)
				}
			}
			for _, env := range c.EnvFrom {
				if cm := env.ConfigMapRef; cm != nil {
					add(configReference{kind: "ConfigMap", name: cm.Name, container: c.Name}, cm.Optional)
				}
				if secret := env.SecretRef; secret != nil {
					add(configReference{kind: "Secret", name: secret.Name, container: c.Name}, secret.Optional)
				}
			}
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if cm := volume.ConfigMap; cm != nil {
			addVolume("ConfigMap", cm.Name, cm.Items, cm.Optional)
		}
		if secret := volume.Secret; secret != nil {
			addVolume("Secret", secret.SecretName, secret.Items, secret.Optional)
		}
		if volume.Projected != nil {
			for _, projection := range volume.Projected.Sources {
				if cm := projection.ConfigMap; cm != nil {
					addVolume("ConfigMap", cm.Name, cm.Items, cm.Optional)
				}
				if secret := projection.Secret; secret != nil {
					addVolume("Secret", secret.Name, secret.Items, secret.Optional)
				}
			}
		}
	}
	return refs
}

type configLocator struct{ namespace, name string }

func analyzeConfiguration(s model.Snapshot, index workloadIndex) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, configurationRule, model.SourcePods)
	configMaps := make(map[configLocator]model.ConfigMap, len(s.ConfigMaps))
	for _, cm := range s.ConfigMaps {
		configMaps[configLocator{cm.Namespace, cm.Name}] = cm
	}
	var findings []model.Finding
	for i := range s.Pods {
		pod := &s.Pods[i]
		if !activePod(pod) {
			continue
		}
		id := model.Identity("Pod", pod)
		incident := index.podIncident(pod)
		issues := make(map[model.ConfigIssue]int)
		for _, ref := range requiredConfiguration(pod) {
			var missing string
			var event *model.Event
			var verified bool
			if ref.kind == "ConfigMap" {
				if cm, ok := configMaps[configLocator{pod.Namespace, ref.name}]; ok {
					verified = true
					if ref.key != "" && !hasConfigKey(cm, ref) {
						missing = "key"
					}
				} else if s.Coverage(model.SourceConfigMaps) == model.Complete {
					verified = true
					missing = "object"
				}
			}
			if missing != "" || !verified {
				event = configReferenceEvent(index, id, pod.CreationTimestamp.Time, ref)
				if !verified && event != nil {
					missing = event.Reference.Missing
				}
			}
			if !verified && event == nil {
				if ref.kind == "Secret" {
					workloadUncertain(&check, "Secret bodies were uninspected and one or more required references have no conclusive event evidence")
				} else {
					workloadUncertain(&check, "A required ConfigMap cannot be verified with available object or event visibility")
				}
			}
			if missing == "" {
				continue
			}
			issue := model.ConfigIssue{Kind: ref.kind, Name: ref.name, Missing: missing}
			if missing == "key" {
				issue.Key = ref.key
			}
			finding := configurationFinding(pod, id, incident, ref, issue, verified, event)
			if previous, ok := issues[issue]; ok {
				if finding.Severity.Rank() > findings[previous].Severity.Rank() {
					findings[previous] = finding
				}
				continue
			}
			issues[issue] = len(findings)
			findings = append(findings, finding)
		}
	}
	return findings, check
}

func hasConfigKey(cm model.ConfigMap, ref configReference) bool {
	for _, key := range cm.Keys {
		if key == ref.key {
			return true
		}
	}
	if ref.volume {
		for _, key := range cm.BinaryKeys {
			if key == ref.key {
				return true
			}
		}
	}
	return false
}

func configReferenceEvent(index workloadIndex, id model.Resource, created time.Time, ref configReference) *model.Event {
	for _, event := range index.events[locateWorkload(id)] {
		issue := event.Reference
		if issue == nil || issue.Kind != ref.kind || issue.Name != ref.name || !eventMatches(event, id, created) || !warningReason(event.Reason) {
			continue
		}
		if issue.Missing == "object" || (issue.Missing == "key" && ref.key != "" && issue.Key == ref.key) {
			return &event
		}
	}
	return nil
}

func configurationFinding(pod *core.Pod, id model.Resource, incident *model.Resource, ref configReference, issue model.ConfigIssue, verified bool, event *model.Event) model.Finding {
	evidence := []model.Evidence{
		workloadEvidence("reference_kind", issue.Kind), workloadEvidence("reference_name", issue.Name), workloadEvidence("missing", issue.Missing),
	}
	if issue.Key != "" {
		evidence = append(evidence, workloadEvidence("reference_key", issue.Key))
	}
	if event != nil && (!verified || event.Reference.Missing == issue.Missing) {
		evidence = addEventEvidence(evidence, *event)
	}
	severity, symptom := model.Warning, "A required configuration object or key is missing from observed configuration"
	if !verified {
		severity, symptom = model.Info, "A recent Pod event reports a missing required configuration object or key"
	}
	if waitingForConfiguration(pod, ref, event) {
		severity = model.Warning
		if event != nil {
			symptom = "A missing required configuration object or key is corroborated by current Pod startup evidence"
		}
	}
	finding := workloadFinding(configurationRule, id, severity, symptom,
		"Compare the named reference and key with the Pod specification and its namespace's configuration. Required references can block startup or mounting.", evidence...)
	finding.Incident = incident
	return finding
}

func waitingForConfiguration(pod *core.Pod, ref configReference, event *model.Event) bool {
	if ref.volume {
		return event != nil && event.Reason == "FailedMount" && pod.Status.Phase == core.PodPending && pod.Spec.NodeName != ""
	}
	for _, statuses := range [][]core.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for _, status := range statuses {
			if status.Name == ref.container && status.State.Waiting != nil && status.State.Waiting.Reason == "CreateContainerConfigError" {
				return true
			}
		}
	}
	return false
}

func analyzePVCs(s model.Snapshot, index workloadIndex) ([]model.Finding, model.CheckResult) {
	check := workloadCoverage(s, pvcBindingRule, model.SourcePVCs)
	classes := make(map[string]storage.StorageClass, len(s.StorageClasses))
	for _, class := range s.StorageClasses {
		classes[class.Name] = class
	}
	assigned := make(map[configLocator]bool)
	for i := range s.Pods {
		pod := &s.Pods[i]
		if !activePod(pod) || pod.Status.Phase == core.PodFailed || pod.Spec.NodeName == "" {
			continue
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				key := configLocator{pod.Namespace, volume.PersistentVolumeClaim.ClaimName}
				assigned[key] = true
			}
		}
	}
	var findings []model.Finding
	for i := range s.PVCs {
		claim := &s.PVCs[i]
		if claim.DeletionTimestamp != nil {
			continue
		}
		id := model.Identity("PersistentVolumeClaim", claim)
		switch claim.Status.Phase {
		case core.ClaimBound:
			continue
		case core.ClaimLost:
			findings = append(findings, workloadFinding(pvcBindingRule, id, model.Critical,
				"The claim reports that its volume binding is lost",
				"Inspect the claim's bound PersistentVolume and storage provisioner before attempting recovery.", workloadEvidence("phase", "Lost")))
		case core.ClaimPending:
			if finding := pendingClaim(s, index, id, claim, classes, assigned[configLocator{claim.Namespace, claim.Name}], &check); finding != nil {
				findings = append(findings, *finding)
			}
		default:
			workloadUncertain(&check, "A claim has no usable binding phase")
		}
	}
	return findings, check
}

func pendingClaim(s model.Snapshot, index workloadIndex, id model.Resource, claim *core.PersistentVolumeClaim, classes map[string]storage.StorageClass, assigned bool, check *model.CheckResult) *model.Finding {
	evidence := []model.Evidence{workloadEvidence("phase", "Pending")}
	evidence = appendObservedTime(evidence, "claim_created", claim.CreationTimestamp.Time)
	provisioningFailure, hasFailure := index.recentEvent(id, claim.CreationTimestamp.Time, "ProvisioningFailed", "FailedBinding")
	if hasFailure {
		evidence = addEventEvidence(evidence, provisioningFailure)
	}
	mode, known := "", false
	if claim.Spec.StorageClassName != nil {
		className := *claim.Spec.StorageClassName
		if className == "" {
			mode, known = "StaticBinding", true
		} else {
			evidence = append(evidence, workloadEvidence("storage_class", className))
			if class, ok := classes[className]; ok {
				mode, known = string(storage.VolumeBindingImmediate), true
				if class.VolumeBindingMode != nil {
					mode = string(*class.VolumeBindingMode)
					known = *class.VolumeBindingMode == storage.VolumeBindingImmediate || *class.VolumeBindingMode == storage.VolumeBindingWaitForFirstConsumer
				}
			} else if storageClassCovered(s, className) {
				mode, known = "MissingStorageClass", true
			}
		}
	}
	if known {
		evidence = append(evidence, workloadEvidence("binding_mode", mode))
	}
	if !known {
		workloadUncertain(check, "A pending claim's StorageClass or binding mode could not be established")
		if !hasFailure {
			return nil
		}
	}
	if mode == string(storage.VolumeBindingWaitForFirstConsumer) && !assigned && claim.Annotations["volume.kubernetes.io/selected-node"] == "" && !hasFailure {
		if s.Coverage(model.SourcePods) != model.Complete {
			workloadUncertain(check, "A WaitForFirstConsumer claim may have consumers outside the visible Pod inventory")
		} else if check.Status == model.Checked && check.Detail == "" {
			check.Detail = "WaitForFirstConsumer explains pending binding for one or more claims without an assigned consumer"
		}
		return nil
	}
	if !hasFailure && !agedEnough(s.CollectedAt, claim.CreationTimestamp.Time, startupGrace) {
		if claim.CreationTimestamp.IsZero() {
			workloadUncertain(check, "A pending claim has no usable creation time")
		}
		return nil
	}
	symptom := "The claim remains pending without a completed volume binding"
	if hasFailure {
		symptom = "The pending claim has a recent binding or provisioning failure event"
	} else if mode == "MissingStorageClass" {
		symptom = "The claim remains pending and its named StorageClass is absent"
	}
	finding := workloadFinding(pvcBindingRule, id, model.Warning, symptom,
		"Inspect the claim events, StorageClass, and available volumes or provisioner. If delayed binding is configured, check the selected consumer's placement.", evidence...)
	return &finding
}

func storageClassCovered(s model.Snapshot, name string) bool {
	for _, source := range s.Sources {
		if source.Source == model.SourceStorageClasses && source.Resource == name {
			return source.Status == model.Complete
		}
	}
	return s.Coverage(model.SourceStorageClasses) == model.Complete
}
