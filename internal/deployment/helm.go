package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
)

// CredentialProvisioner issues an OpenBao lease scoped to the configured target
// and materializes its kubeconfig as key "config" in an ephemeral Secret in the
// execution namespace. Prepare must be idempotent per attempt (operation id +
// attempt number). Observation gets a separate lease, never revoking an in-flight
// upgrade lease. Revoke is idempotent and revokes/deletes only that attempt's lease/Secret.
// A successful Revoke MUST durably fence late/in-flight Prepare for that identity,
// including ambiguous issuance: missing Secret/lease or elapsed TTL is not enough.
// Prepare may partially issue on any error; callers always compensate it.
// Secret values never enter this API. A lease expiry is NOT proof the target
// rejects the issued credential during an OpenBao outage: the provider adapter
// must establish target-enforced expiry separately before production activation.
// The provisioner MUST independently bind op.OrgID/RepoID/Target/Environment to
// its approved OpenBao role; the operation is not authority to choose a role.
type CredentialProvisioner interface {
	Prepare(context.Context, Operation, string, time.Duration) (Credential, error)
	Revoke(context.Context, Operation, string) error
}
type Credential struct {
	SecretName string
	// ExpiresAt is the provider lease horizon, NOT a target credential cutoff.
	ExpiresAt time.Time
}

// HelmConfig is operator-owned. Image bundles the trusted chart and
// deployment-runner wrapper, and is pinned by digest. ServiceAccount has NO
// deployment privileges: the sole target credential is the OpenBao kubeconfig.
// NetworkPolicy for ExecutionNamespace must limit egress to the target API.
type HelmConfig struct {
	// TargetClusterID is the operator canonical cluster identity, not a role or revision.
	TargetClusterID    string
	ExecutionNamespace string
	TargetNamespace    string
	Release            string
	Image              string
	ChartPath          string
	ArtifactValueKey   string
	ServiceAccount     string
	// CredentialPolicyRevision identifies the immutable OpenBao cluster/role
	// mapping. Change it whenever that mapping changes.
	CredentialPolicyRevision string
}

type HelmExecutor struct {
	client      kubernetes.Interface
	config      HelmConfig
	credentials CredentialProvisioner
}

var pinnedImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9./:_-]*@sha256:[a-f0-9]{64}$`)

func NewHelmExecutor(client kubernetes.Interface, config HelmConfig, credentials CredentialProvisioner) (*HelmExecutor, error) {
	if client == nil || credentials == nil || strings.TrimSpace(config.TargetClusterID) == "" || config.CredentialPolicyRevision == "" || len(validation.IsDNS1123Label(config.ExecutionNamespace)) > 0 || len(validation.IsDNS1123Label(config.TargetNamespace)) > 0 || len(validation.IsDNS1123Subdomain(config.ServiceAccount)) > 0 || len(validation.IsDNS1123Label(config.Release)) > 0 || len(config.Release) > 53 || !pinnedImage.MatchString(config.Image) || !strings.HasPrefix(config.ChartPath, "/charts/") || path.Clean(config.ChartPath) != config.ChartPath || !strings.HasSuffix(config.ChartPath, ".tgz") || !valueKey.MatchString(config.ArtifactValueKey) {
		return nil, errors.New("invalid fixed Helm executor configuration")
	}
	return &HelmExecutor{client: client, config: config, credentials: credentials}, nil
}

// Destination excludes executor/chart/credential revisions so aliases and upgrades
// cannot evade durable exclusion. JSON tuple encoding prevents delimiter aliases.
func (h *HelmExecutor) Destination() string {
	b, _ := json.Marshal([]string{h.config.TargetClusterID, h.config.TargetNamespace, h.config.Release})
	return string(b)
}

// Revision binds the entire fixed target configuration into approval intent.
func (h *HelmExecutor) Revision() string {
	b, _ := json.Marshal(h.config)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func helmBinding(op Operation) string {
	sum := sha256.Sum256([]byte(binding(op) + "/delivery/" + strconv.Itoa(latestDelivery(op))))
	return "novaforge:" + op.ID.String() + ":" + hex.EncodeToString(sum[:])
}

func (h *HelmExecutor) Execute(ctx context.Context, op Operation) (Result, error) {
	return h.run(ctx, op, false)
}
func (h *HelmExecutor) Observe(ctx context.Context, op Operation) (Result, error) {
	// Cleanup is provider access too: validate the current durable attempt
	// before touching earlier obligations, not only before preparing a new one.
	if op.ID == uuid.Nil || op.TargetRevision != h.Revision() || len(op.Attempts) == 0 || op.journal.require(ctx, op, h.Revision()) != nil {
		return Result{}, ErrUncertain
	}
	// Retry only pre-existing obligations; active/ambiguous Jobs keep their
	// credentials. A status observation may still collect evidence meanwhile.
	for i, c := range op.Credentials {
		if c.ResolvedAt == nil && c.Attempt != op.Attempts[len(op.Attempts)-1].Number {
			if h.cleanupCredential(ctx, op, c) == nil {
				now := time.Now()
				op.Credentials[i].ResolvedAt = &now
			}
		}
	}
	if h.preparationStopped(ctx, op) {
		// This observation's obligation was registered before entering the executor,
		// but it needs no credential. Fence that unissued identity as well.
		if err := h.revoke(ctx, op); err != nil {
			return Result{}, ErrUncertain
		}
		return Result{Summary: "credential preparation ended before dispatch"}, ErrDeliveryFailed
	}
	result, err := h.run(ctx, op, true)
	if err != nil && !errors.Is(err, ErrDeliveryFailed) {
		return result, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	return result, err
}

func (h *HelmExecutor) run(ctx context.Context, op Operation, observe bool) (Result, error) {
	if op.ID == uuid.Nil || op.TargetRevision != h.Revision() || len(op.Attempts) == 0 || !artifactDigest.MatchString(op.Artifact) {
		return Result{}, errors.New("unbound Helm operation")
	}
	if err := op.journal.require(ctx, op, h.Revision()); err != nil {
		return Result{}, fmt.Errorf("%w: credential obligation unavailable", ErrUncertain)
	}
	attempt := op.Attempts[len(op.Attempts)-1].Number
	name := "nf-deploy-" + op.ID.String() + "-" + strconv.Itoa(attempt)
	mode := "execute"
	if observe {
		mode = "observe"
	}
	jobs := h.client.BatchV1().Jobs(h.config.ExecutionNamespace)
	job, err := jobs.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cred, credentialErr := h.credentials.Prepare(ctx, op, h.config.ExecutionNamespace, 10*time.Minute)
		if credentialErr != nil {
			revokeErr := h.revoke(ctx, op)
			if revokeErr != nil || errors.Is(credentialErr, ErrUncertain) || ctx.Err() != nil {
				return Result{}, fmt.Errorf("%w: deployment credential issuance failed", ErrUncertain)
			}
			return Result{}, errors.New("deployment credential issuance failed")
		}
		if cred.SecretName != CredentialSecretName(op) || len(validation.IsDNS1123Subdomain(cred.SecretName)) > 0 || cred.ExpiresAt.Before(time.Now().Add(5*time.Minute)) || cred.ExpiresAt.After(time.Now().Add(11*time.Minute)) {
			revokeErr := h.revoke(ctx, op)
			if revokeErr != nil {
				return Result{}, fmt.Errorf("%w: invalid deployment credential could not be revoked", ErrUncertain)
			}
			return Result{}, errors.New("deployment credential lease does not cover the execution window")
		}
		if err = op.journal.update(ctx, op, h.Revision(), true); err != nil {
			// No create was attempted; compensate even if cancellation/session loss
			// prevents the durable acknowledgement. The original obligation stays.
			_ = h.revoke(ctx, op)
			return Result{}, ErrUncertain
		}
		job, err = jobs.Create(ctx, h.job(op, mode, name, cred.SecretName), metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			job, err = jobs.Get(ctx, name, metav1.GetOptions{})
		}
	}
	// A create timeout is ambiguous: never release/reissue credentials or submit
	// another upgrade until the deterministic Job or remote release is observed.
	if err != nil {
		return Result{}, fmt.Errorf("%w: cannot establish executor job", ErrUncertain)
	}
	if !h.matches(job, op, mode) {
		return Result{}, fmt.Errorf("%w: executor job binding mismatch", ErrUncertain)
	}
	if err = op.journal.update(ctx, op, h.Revision(), true); err != nil {
		return Result{}, ErrUncertain
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		terminal := false
		for _, c := range job.Status.Conditions {
			if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
				terminal = true
			}
		}
		if terminal {
			result, resultErr := h.evidence(ctx, job, op)
			// Revoke only after validating the actual executor Pod has stopped;
			// a terminal Job condition alone does not fence a live executor.
			if _, err := h.terminalPod(ctx, job, op); err != nil {
				return result, ErrUncertain
			}
			revokeErr := h.revoke(ctx, op)
			if revokeErr != nil {
				return Result{}, fmt.Errorf("%w: deployment credential revocation failed", ErrUncertain)
			}
			return result, resultErr
		}
		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("%w: executor job observation interrupted", ErrUncertain)
		case <-ticker.C:
		}
		job, err = jobs.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Result{}, fmt.Errorf("%w: executor job unavailable", ErrUncertain)
		}
		if !h.matches(job, op, mode) {
			return Result{}, fmt.Errorf("%w: executor job binding changed", ErrUncertain)
		}
	}
}

func (h *HelmExecutor) job(op Operation, mode, name, secret string) *batchv1.Job {
	no := false
	yes := true
	uid := int64(65532)
	modeBits := int32(0440)
	zero := int32(0)
	deadline := int64(300)
	args := []string{mode, h.config.Release, h.config.ChartPath, h.config.TargetNamespace, h.config.ArtifactValueKey, op.Artifact, helmBinding(op), previousDeliveryBinding(op)}
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.config.ExecutionNamespace, Labels: map[string]string{"novaforge.dev/deployment": op.ID.String()}, Annotations: map[string]string{"novaforge.dev/binding": helmBinding(op)}}, Spec: batchv1.JobSpec{
		BackoffLimit: &zero, ActiveDeadlineSeconds: &deadline,
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"novaforge.dev/deployment": op.ID.String()}}, Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: h.config.ServiceAccount, AutomountServiceAccountToken: &no, EnableServiceLinks: &no,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "helm", Image: h.config.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/usr/local/bin/deployment-runner"}, Args: args,
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Env:             []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}, {Name: "HELM_CACHE_HOME", Value: "/tmp/cache"}, {Name: "HELM_CONFIG_HOME", Value: "/tmp/config"}, {Name: "HELM_DATA_HOME", Value: "/tmp/data"}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "credentials", MountPath: "/credentials", ReadOnly: true}, {Name: "scratch", MountPath: "/tmp"}}, Resources: helmResources()}},
			Volumes: []corev1.Volume{{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret, DefaultMode: &modeBits, Items: []corev1.KeyToPath{{Key: "config", Path: "config"}}}}}, {Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}}},
		}}}}
}

func (h *HelmExecutor) matches(job *batchv1.Job, op Operation, mode string) bool {
	spec := job.Spec.Template.Spec
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 300 ||
		(job.Spec.Parallelism != nil && *job.Spec.Parallelism != 1) ||
		(job.Spec.Completions != nil && *job.Spec.Completions != 1) {
		return false
	}
	if job.Namespace != h.config.ExecutionNamespace || job.Annotations["novaforge.dev/binding"] != helmBinding(op) || len(spec.Containers) != 1 || len(spec.InitContainers) != 0 || len(spec.EphemeralContainers) != 0 || len(spec.Volumes) != 2 || spec.Volumes[0].Secret == nil || spec.Volumes[0].Secret.SecretName != CredentialSecretName(op) || spec.HostNetwork || spec.HostPID || spec.HostIPC {
		return false
	}
	expected := h.job(op, mode, job.Name, spec.Volumes[0].Secret.SecretName).Spec.Template.Spec
	return reflect.DeepEqual(normalizedPodSpec(spec), normalizedPodSpec(expected)) && job.Spec.BackoffLimit != nil && *job.Spec.BackoffLimit == 0
}

// Normalize only documented API defaults and scheduling assignments. Admission
// changes to execution, namespaces, DNS, credentials or security fail closed.
// This is shared by the template and actual evidence-bearing Pod checks.
func normalizedPodSpec(spec corev1.PodSpec) *corev1.PodSpec {
	p := spec.DeepCopy()
	if p.DNSPolicy == "" {
		p.DNSPolicy = corev1.DNSClusterFirst
	}
	if p.SchedulerName == "" {
		p.SchedulerName = "default-scheduler"
	}
	if p.TerminationGracePeriodSeconds == nil {
		v := int64(30)
		p.TerminationGracePeriodSeconds = &v
	}
	if p.DeprecatedServiceAccount == p.ServiceAccountName {
		p.DeprecatedServiceAccount = ""
	}
	p.NodeName = "" // scheduler-assigned; never target authority
	if p.Priority != nil && *p.Priority == 0 {
		p.Priority = nil
	}
	if p.PreemptionPolicy != nil && *p.PreemptionPolicy == corev1.PreemptLowerPriority {
		p.PreemptionPolicy = nil
	}
	tolerations := p.Tolerations[:0]
	for _, t := range p.Tolerations {
		if (t.Key == "node.kubernetes.io/not-ready" || t.Key == "node.kubernetes.io/unreachable") && t.Operator == corev1.TolerationOpExists && t.Value == "" && t.Effect == corev1.TaintEffectNoExecute && t.TolerationSeconds != nil && *t.TolerationSeconds == 300 {
			continue
		}
		tolerations = append(tolerations, t)
	}
	if len(tolerations) == 0 {
		p.Tolerations = nil
	} else {
		p.Tolerations = tolerations
	}
	for i := range p.Containers {
		c := &p.Containers[i]
		if c.TerminationMessagePath == "" {
			c.TerminationMessagePath = "/dev/termination-log"
		}
		if c.TerminationMessagePolicy == "" {
			c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
		}
	}
	return p
}

func (h *HelmExecutor) evidence(ctx context.Context, job *batchv1.Job, op Operation) (Result, error) {
	if op.Attempts[len(op.Attempts)-1].Kind == "observe" {
		// Even exact status evidence is not a fence against a live delivery process.
		delivery, prior, err := h.existingDelivery(ctx, op)
		if err != nil {
			return Result{}, ErrUncertain
		}
		if _, err = h.terminalPod(ctx, delivery, prior); err != nil {
			return Result{}, ErrUncertain
		}
	}
	pod, err := h.terminalPod(ctx, job, op)
	if err != nil {
		return Result{}, err
	}
	status := pod.Status.ContainerStatuses[0]
	stream, err := h.client.CoreV1().Pods(job.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "helm"}).Stream(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%w: executor evidence unavailable", ErrUncertain)
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, 8193))
	if err != nil || len(data) > 8192 {
		return Result{}, fmt.Errorf("%w: executor evidence truncated", ErrUncertain)
	}
	var evidence HelmEvidence
	if err = json.Unmarshal(data, &evidence); err != nil || evidence.Binding != helmBinding(op) || evidence.Release != h.config.Release || evidence.Namespace != h.config.TargetNamespace || evidence.Revision < 1 {
		return Result{}, fmt.Errorf("%w: executor evidence does not identify this release operation", ErrUncertain)
	}
	result := Result{ExternalID: evidence.Release + "/" + strconv.Itoa(evidence.Revision), Summary: "Helm " + evidence.State + "; " + evidence.Binding}
	switch evidence.State {
	case StateSucceeded:
		if pod.Status.Phase != corev1.PodSucceeded || status.State.Terminated.ExitCode != 0 {
			return Result{}, fmt.Errorf("%w: Helm readiness was not confirmed by a successful job", ErrUncertain)
		}
		return result, nil
	case StateFailed:
		return result, fmt.Errorf("%w: Helm release recorded a failed deployment", ErrDeliveryFailed)
	default:
		return result, ErrUncertain
	}
}

// terminalPod validates executor identity and actual termination independently
// of release logs, so cleanup is safe even when those logs are missing.
func (h *HelmExecutor) terminalPod(ctx context.Context, job *batchv1.Job, op Operation) (*corev1.Pod, error) {
	pods, err := h.client.CoreV1().Pods(job.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/job-name=" + job.Name})
	if err != nil {
		return nil, fmt.Errorf("%w: executor pods unavailable", ErrUncertain)
	}
	var owned []corev1.Pod
	for _, pod := range pods.Items {
		for _, owner := range pod.OwnerReferences {
			if owner.UID == job.UID && owner.Kind == "Job" && owner.Controller != nil && *owner.Controller {
				owned = append(owned, pod)
			}
		}
	}
	if len(owned) != 1 || job.UID == "" {
		return nil, fmt.Errorf("%w: executor pod identity is ambiguous", ErrUncertain)
	}
	if owned[0].Status.Phase != corev1.PodSucceeded && owned[0].Status.Phase != corev1.PodFailed {
		return nil, fmt.Errorf("%w: executor pod is not terminal", ErrUncertain)
	}
	pod := &owned[0]
	actual := job.DeepCopy()
	actual.Spec.Template.Spec = pod.Spec
	mode := job.Spec.Template.Spec.Containers[0].Args[0]
	if pod.Namespace != job.Namespace || !h.matches(actual, op, mode) || len(pod.Status.ContainerStatuses) != 1 {
		return nil, fmt.Errorf("%w: executor pod contract mismatch", ErrUncertain)
	}
	status := pod.Status.ContainerStatuses[0]
	// Runtime image identifiers may include a transport prefix or repository.
	// The digest must still equal the configured immutable image's digest.
	imageID := status.ImageID
	if i := strings.Index(imageID, "://"); i >= 0 {
		imageID = imageID[i+3:]
	}
	if i := strings.LastIndex(imageID, "@"); i >= 0 {
		imageID = imageID[i+1:]
	}
	digest := strings.Split(h.config.Image, "@")[1]
	if status.Name != "helm" || status.Image != h.config.Image || imageID != digest || status.State.Terminated == nil || status.RestartCount != 0 {
		return nil, fmt.Errorf("%w: executor container identity unavailable", ErrUncertain)
	}
	return pod, nil
}

// Observation attempts refer to the newest execute attempt, never an older
// delivery whose revision happens still to be returned by Helm status.
func latestDelivery(op Operation) int {
	for i := len(op.Attempts) - 1; i >= 0; i-- {
		if op.Attempts[i].Kind == "execute" {
			return op.Attempts[i].Number
		}
	}
	return 0
}

func previousDeliveryBinding(op Operation) string {
	current := helmBinding(op)
	for i := len(op.Attempts) - 2; i >= 0; i-- {
		prior := op
		prior.Attempts = op.Attempts[:i+1]
		if op.Attempts[i].Result.ExternalID != "" && helmBinding(prior) != current {
			return helmBinding(prior)
		}
	}
	return "-"
}

// CredentialSecretName is the only Secret an executor attempt may mount.
func CredentialSecretName(op Operation) string {
	if len(op.Attempts) == 0 {
		return ""
	}
	return "nf-deploy-" + op.ID.String() + "-" + strconv.Itoa(op.Attempts[len(op.Attempts)-1].Number) + "-creds"
}

// ObserveExisting is deliberately passive: an administrator can recover logged
// evidence after the author grant expires, but cannot start a status Job or
// acquire new target credentials. Missing logs or active jobs remain unknown.
func (h *HelmExecutor) ObserveExisting(ctx context.Context, op Operation) (Result, error) {
	if op.TargetRevision != h.Revision() || len(op.Attempts) < 2 {
		return Result{}, ErrUncertain
	}
	if h.preparationStopped(ctx, op) {
		return Result{Summary: "credential preparation ended before dispatch"}, ErrDeliveryFailed
	}
	job, prior, err := h.existingDelivery(ctx, op)
	if err != nil {
		return Result{}, err
	}
	return h.evidence(ctx, job, prior)
}

// Never skip a newer execute even if its Job/logs are absent. Observation and
// administrative recovery attempts cannot substitute for a delivery identity.
func (h *HelmExecutor) existingDelivery(ctx context.Context, op Operation) (*batchv1.Job, Operation, error) {
	prior := op
	if len(prior.Attempts) > 0 {
		prior.Attempts = prior.Attempts[:len(prior.Attempts)-1]
	}
	for len(prior.Attempts) > 0 && prior.Attempts[len(prior.Attempts)-1].Kind != "execute" {
		prior.Attempts = prior.Attempts[:len(prior.Attempts)-1]
	}
	if len(prior.Attempts) == 0 {
		return nil, Operation{}, ErrUncertain
	}
	attempt := prior.Attempts[len(prior.Attempts)-1]
	name := "nf-deploy-" + op.ID.String() + "-" + strconv.Itoa(attempt.Number)
	job, err := h.client.BatchV1().Jobs(h.config.ExecutionNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || !h.matches(job, prior, attempt.Kind) {
		return nil, Operation{}, ErrUncertain
	}
	for _, c := range job.Status.Conditions {
		if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
			return job, prior, nil
		}
	}
	return nil, Operation{}, ErrUncertain
}

// A confirmed no-dispatch obligation is also durable failure evidence. This is
// the only no-Job recovery path; absence alone never establishes non-delivery.
func (h *HelmExecutor) preparationStopped(ctx context.Context, op Operation) bool {
	for _, c := range op.Credentials {
		if c.Attempt == latestDelivery(op) {
			if c.Phase != "preparing" || c.ResolvedAt == nil {
				return false
			}
			name := "nf-deploy-" + op.ID.String() + "-" + strconv.Itoa(c.Attempt)
			_, err := h.client.BatchV1().Jobs(h.config.ExecutionNamespace).Get(ctx, name, metav1.GetOptions{})
			return apierrors.IsNotFound(err)
		}
	}
	return false
}
