package gates

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gitv1 "github.com/novaforge/novaforge/gen/novaforge/git/v1"
	reviewsv1 "github.com/novaforge/novaforge/gen/novaforge/reviews/v1"
	"github.com/novaforge/novaforge/internal/analysis"
	"github.com/novaforge/novaforge/internal/authz"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type sandboxGit struct {
	gitv1.GitServiceClient
	repo     uuid.UUID
	blobRefs *[]string
}

func (g sandboxGit) ListRepos(context.Context, *gitv1.ListReposRequest, ...grpc.CallOption) (*gitv1.ListReposResponse, error) {
	return &gitv1.ListReposResponse{Repos: []*gitv1.Repo{{Id: g.repo.String(), Name: "hostile"}}}, nil
}
func (g sandboxGit) GetTree(context.Context, *gitv1.GetTreeRequest, ...grpc.CallOption) (*gitv1.GetTreeResponse, error) {
	return &gitv1.GetTreeResponse{}, nil
}

func (g sandboxGit) GetBlob(ctx context.Context, req *gitv1.GetBlobRequest, opts ...grpc.CallOption) (*gitv1.GetBlobResponse, error) {
	if g.blobRefs != nil {
		*g.blobRefs = append(*g.blobRefs, req.Ref)
	}
	return &gitv1.GetBlobResponse{Content: []byte("paths: {}\n")}, nil
}

func TestWorkspaceInputRefusesPrivilegedFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(authz.WithScope(context.Background(), authz.Scope{OrgID: uuid.New()}))
	defer cancel()
	scope, _ := authz.FromContext(ctx)
	repo := uuid.New()
	_, err := NewWorkspaceInputBuilder(sandboxGit{repo: repo}, "")(ctx, uuid.New(), RunHead{OrgID: scope.OrgID, RepoID: repo, HeadSHA: "0123456789012345678901234567890123456789"}, "tests", nil)
	if err == nil {
		t.Fatal("unconfigured sandbox admitted privileged tenant execution")
	}
}

func TestAnalysisSandboxPodBoundary(t *testing.T) {
	image := "registry/analysis@sha256:" + strings.Repeat("a", 64)
	p := gateSandboxPod("nf-gate-test", image, nil, nil)
	if *p.Spec.AutomountServiceAccountToken || *p.Spec.EnableServiceLinks || p.Spec.HostNetwork || p.Spec.HostPID || p.Spec.HostIPC {
		t.Fatal("host/service authority enabled")
	}
	if !*p.Spec.SecurityContext.RunAsNonRoot || p.Spec.SecurityContext.SeccompProfile.Type != "RuntimeDefault" {
		t.Fatal("pod security boundary missing")
	}
	if *p.Spec.ActiveDeadlineSeconds != 600 || len(p.Spec.Containers) != 1 {
		t.Fatal("unbounded execution")
	}
	c := p.Spec.Containers[0]
	if c.Image != image || !*c.SecurityContext.ReadOnlyRootFilesystem || *c.SecurityContext.AllowPrivilegeEscalation || len(c.EnvFrom) != 0 {
		t.Fatal("container isolation missing")
	}
	for _, v := range p.Spec.Volumes {
		if v.EmptyDir == nil || v.EmptyDir.SizeLimit == nil {
			t.Fatal("non-scratch or unbounded volume")
		}
	}
	for _, e := range c.Env {
		if e.ValueFrom != nil || strings.Contains(e.Name, "TOKEN") || strings.Contains(e.Name, "DATABASE") {
			t.Fatal("credential environment")
		}
	}
	for k, v := range p.Labels {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			t.Fatal(errs)
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			t.Fatal(errs)
		}
	}
}

func TestAnalysisSnapshotRejectsSymlinksAndOversize(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := gateSnapshot(dir); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Remove(filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "large"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(sandboxSourceLimit + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := gateSnapshot(dir); err == nil {
		t.Fatal("oversize snapshot accepted")
	}
}

func TestAnalysisSandboxRejectsForeignScopeAndMutableImage(t *testing.T) {
	client := fake.NewClientset()
	if _, err := NewAnalysisSandbox(client, &rest.Config{}, "analysis:latest"); err == nil {
		t.Fatal("mutable image accepted")
	}
	s, err := NewAnalysisSandbox(client, &rest.Config{}, "registry/analysis@sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	org := uuid.New()
	head := RunHead{OrgID: org, RepoID: uuid.New(), HeadSHA: strings.Repeat("a", 40)}
	ctx := authz.WithScope(context.Background(), authz.Scope{OrgID: org})
	run, err := s.bind(ctx, t.TempDir(), uuid.New(), head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.bind(context.Background(), t.TempDir(), uuid.New(), head); err == nil {
		t.Fatal("unscoped bind")
	}
	if _, _, err := run(authz.WithScope(ctx, authz.Scope{OrgID: uuid.New()}), "wrong", "go", "test"); err == nil {
		t.Fatal("foreign run accepted")
	}
	if len(client.Actions()) != 0 {
		t.Fatal("invalid identity reached cluster")
	}
}

func TestAnalysisSandboxOwnedClusterHostileRepository(t *testing.T) {
	image := os.Getenv("NF_GATE_SANDBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("explicit owned-cluster image required")
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: "kw"}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewAnalysisSandbox(client, config, image)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{"go.mod": "module hostile.test/probe\n\ngo 1.22\n", "probe.go": "package probe\nfunc Safe() bool {return true}\n", "probe_test.go": `package probe
import("os";"net";"testing";"time")
func TestAuthority(t *testing.T){
 if os.Getenv("NF_GATE_CONTROL_PLANE_SECRET")!="" {t.Fatal("inherited control-plane credential")}
 if _,err:=os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token");!os.IsNotExist(err){t.Fatal("serviceaccount token accessible")}
 if c,err:=net.DialTimeout("tcp","192.168.10.121:5432",time.Second);err==nil {c.Close();t.Fatal("database egress available")}
 if c,err:=net.DialTimeout("tcp","1.1.1.1:443",time.Second);err==nil {c.Close();t.Fatal("internet egress available")}
 if !Safe(){t.Fatal("bad result")}
}
`}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NF_GATE_CONTROL_PLANE_SECRET", "owned-hostile-sentinel")
	org := uuid.New()
	ctx, cancel := context.WithTimeout(authz.WithScope(context.Background(), authz.Scope{OrgID: org}), 5*time.Minute)
	defer cancel()
	run, err := s.bind(ctx, dir, uuid.New(), RunHead{OrgID: org, RepoID: uuid.New(), HeadSHA: strings.Repeat("b", 40)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := analysis.Tests(ctx, run, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || !result.CoverageAvailable {
		t.Fatalf("hostile probe: %+v", result)
	}
	t.Logf("isolated hostile tests passed; coverage %.1f; immutable image %s", result.Coverage, image)
}

func TestArchitectureUsesIsolatedExecutor(t *testing.T) {
	called := false
	in := Input{WorkDir: t.TempDir(), Params: map[string]any{"forbidden_dependencies": []string{"app -> forbidden"}}, Exec: func(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
		called = true
		if name != "go" || strings.Join(args, " ") != "list -deps -json ./..." {
			t.Fatalf("unexpected compiler command %s %v", name, args)
		}
		return []byte(`{"ImportPath":"example/app","Name":"app","Imports":["example/forbidden"]}{"ImportPath":"example/forbidden","Name":"forbidden"}`), 0, nil
	}}
	result, err := runArchitecture(context.Background(), in)
	if !called || err != nil || result.Status != "fail" {
		t.Fatalf("compiler bypassed isolated executor: called=%v result=%+v err=%v", called, result, err)
	}
}

func TestAPICompatUnavailableIsNotAbsence(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy string
		exit   int
		want   string
	}{
		{"missing policy", "", 0, "error"}, {"git unavailable", strings.Repeat("a", 40), 128, "error"}, {"confirmed absence", strings.Repeat("a", 40), 44, "pass"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := runAPICompat(context.Background(), Input{PolicySHA: test.policy, SourceSHA: strings.Repeat("b", 40), Exec: func(context.Context, string, string, ...string) ([]byte, int, error) { return nil, test.exit, nil }})
			if err != nil || result.Status != test.want {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

func TestArchitectureRejectsIncompleteEvidence(t *testing.T) {
	for _, out := range []string{"", `{"ImportPath":"a","Name":"a","Imports":["missing"]}`, `{"ImportPath":"a","Name":"a","Incomplete":true}`, `{"ImportPath":"a","Name":"a"} garbage`} {
		result, err := runArchitecture(context.Background(), Input{WorkDir: t.TempDir(), Exec: func(context.Context, string, string, ...string) ([]byte, int, error) { return []byte(out), 0, nil }})
		if err != nil || result.Status != "error" {
			t.Fatalf("accepted incomplete graph %q: %+v %v", out, result, err)
		}
	}
}

func TestAnalysisOutputBound(t *testing.T) {
	var b sandboxBuffer
	if _, err := b.Write(make([]byte, sandboxOutputLimit)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("x")); err == nil {
		t.Fatal("output truncation accepted as complete")
	}
}

type sandboxReviews struct {
	reviewsv1.ReviewsServiceClient
	seen context.Context
}

func (r *sandboxReviews) RecordProof(ctx context.Context, req *reviewsv1.RecordProofRequest, opts ...grpc.CallOption) (*reviewsv1.RecordProofResponse, error) {
	r.seen = ctx
	return &reviewsv1.RecordProofResponse{}, nil
}
func TestGateProofRequiresScopedSigner(t *testing.T) {
	reviews := &sandboxReviews{}
	controller := NewController(nil, nil, reviews, nil, "")
	if err := controller.Proof(context.Background(), uuid.New(), "tests", "pass", "evidence"); err == nil || reviews.seen != nil {
		t.Fatal("unsigned proof admitted")
	}
	type key struct{}
	signed := context.WithValue(context.Background(), key{}, "signed-only")
	controller = NewController(nil, nil, reviews, nil, "", WithProofContext(func(context.Context) (context.Context, error) { return signed, nil }))
	if err := controller.Proof(context.Background(), uuid.New(), "tests", "pass", "evidence"); err != nil {
		t.Fatal(err)
	}
	if reviews.seen != signed {
		t.Fatal("original caller forwarded instead of scoped signer")
	}
}

func TestBoundAPIReadsExactSourceAndPolicy(t *testing.T) {
	client := fake.NewClientset()
	sandbox, err := NewAnalysisSandbox(client, &rest.Config{}, "registry/analysis@sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	org, repo := uuid.New(), uuid.New()
	ctx, cancel := context.WithCancel(authz.WithScope(context.Background(), authz.Scope{OrgID: org}))
	defer cancel()
	var refs []string
	head := RunHead{OrgID: org, RepoID: repo, HeadSHA: strings.Repeat("b", 40), TargetSHA: strings.Repeat("c", 40)}
	in, err := NewWorkspaceInputBuilder(sandboxGit{repo: repo, blobRefs: &refs}, "", sandbox)(ctx, uuid.New(), head, "api-compatibility", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runAPICompat(ctx, in)
	if err != nil || result.Status != "pass" {
		t.Fatalf("%+v %v", result, err)
	}
	if strings.Join(refs, ",") != head.TargetSHA+","+head.HeadSHA {
		t.Fatalf("wrong revision evidence: %v", refs)
	}
	if _, _, err := in.Exec(ctx, in.WorkDir, "git", "show", strings.Repeat("d", 40)+":"+openapiSpecPath); err == nil {
		t.Fatal("unbound revision admitted")
	}
	if len(client.Actions()) != 0 {
		t.Fatal("read-only Git evidence launched tenant processes")
	}
}
