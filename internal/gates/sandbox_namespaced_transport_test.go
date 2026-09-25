package gates_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/gates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

// Local HTTP serialization/REST transport plus a client-go tracker is protocol
// evidence only. Neither it nor the fake qualifies Kubernetes admission/isolation.
func newNamespacedFor(t *testing.T, j *gates.SandboxJournal, c *fake.Clientset) (*gates.NamespacedSandbox, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		prefix := "/api/v1/namespaces/gate-sandbox/pods"
		if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
			t.Errorf("forbidden API path %s", r.URL.Path)
			http.Error(w, "forbidden", 403)
			return
		}
		name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
		if strings.Contains(name, "/") {
			t.Errorf("forbidden subresource %s", name)
			http.Error(w, "forbidden", 403)
			return
		}
		pods := c.CoreV1().Pods("gate-sandbox")
		var result any
		var err error
		switch r.Method {
		case http.MethodPost:
			var p corev1.Pod
			err = json.NewDecoder(r.Body).Decode(&p)
			if err == nil {
				result, err = pods.Create(r.Context(), &p, metav1.CreateOptions{})
			}
		case http.MethodGet:
			result, err = pods.Get(r.Context(), name, metav1.GetOptions{})
		case http.MethodDelete:
			var opts metav1.DeleteOptions
			err = json.NewDecoder(r.Body).Decode(&opts)
			if err == nil {
				err = pods.Delete(r.Context(), name, opts)
			}
			result = &metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success", Code: 200}
		default:
			t.Errorf("forbidden method %s", r.Method)
			http.Error(w, "forbidden", 403)
			return
		}
		if err != nil {
			status := metav1.Status{Status: "Failure", Code: 500, Reason: metav1.StatusReasonInternalError, Message: "test API error"}
			var apiStatus apierrors.APIStatus
			if errors.As(err, &apiStatus) {
				status = apiStatus.Status()
			}
			status.APIVersion = "v1"
			status.Kind = "Status"
			w.WriteHeader(int(status.Code))
			_ = json.NewEncoder(w).Encode(status)
			return
		}
		if p, ok := result.(*corev1.Pod); ok {
			p.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(server.Close)
	return gates.NewNamespacedSandbox(j, &rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}, namespacedConfig())
}

func TestNamespacedSandboxCreateNoHTTPReplay(t *testing.T) {
	for _, code := range []int{401, 429, 500, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			j, pool, target := journalFor(t, 1)
			ctx := scopedCtx(uuid.New())
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/namespaces/gate-sandbox/pods" {
					t.Errorf("unexpected API %s %s", r.Method, r.URL.Path)
				}
				posts.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(code)
				_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: "Failure", Reason: metav1.StatusReasonTooManyRequests, Code: int32(code)})
			}))
			defer server.Close()
			s, err := gates.NewNamespacedSandbox(j, &rest.Config{Host: server.URL}, namespacedConfig())
			sandboxOK(t, err)
			intent := sandboxIntent()
			id, uid, err := s.Allocate(ctx, intent)
			if err == nil || uid != "" {
				t.Fatal("retryable response accepted")
			}
			v, err := j.ReadExact(ctx, id)
			sandboxOK(t, err)
			if v.CreateClaim == nil || v.PodUID != nil || v.Released {
				t.Fatal("uncertainty not retained")
			}
			_, _, err = s.Allocate(ctx, intent)
			sandboxError(t, err, gates.ErrSandboxClaimed)
			if posts.Load() != 1 {
				t.Fatalf("physical POST count=%d; want exactly one", posts.Load())
			}
			sandboxCount(t, pool, target, 1)
		})
	}
}

func TestNamespacedSandboxCreateReplyLost(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx, cancel := context.WithTimeout(scopedCtx(uuid.New()), 5*time.Second)
	defer cancel()
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		var p corev1.Pod
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close() // body received; no response acknowledgement
	}))
	defer server.Close()
	s, err := gates.NewNamespacedSandbox(j, &rest.Config{Host: server.URL}, namespacedConfig())
	sandboxOK(t, err)
	intent := sandboxIntent()
	id, uid, err := s.Allocate(ctx, intent)
	if err == nil || uid != "" {
		t.Fatal("lost response exposed usable pod")
	}
	_, _, err = s.Allocate(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxClaimed)
	v, err := j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if posts.Load() != 1 || v.PodUID != nil || v.CreateClaim == nil || v.Released {
		t.Fatal("lost response replayed or released")
	}
	sandboxCount(t, pool, target, 1)
}

func TestNamespacedSandboxCreateNoRedirect(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	s, err := gates.NewNamespacedSandbox(j, &rest.Config{Host: server.URL}, namespacedConfig())
	sandboxOK(t, err)
	_, uid, err := s.Allocate(ctx, sandboxIntent())
	if err == nil || uid != "" || posts.Load() != 1 {
		t.Fatalf("redirect replay: requests=%d UID=%q err=%v", posts.Load(), uid, err)
	}
	sandboxCount(t, pool, target, 1)
}

func TestNamespacedSandboxCreateCancellation(t *testing.T) {
	j, pool, target := journalFor(t, 1)
	ctx := scopedCtx(uuid.New())
	c := namespacedClient(t)
	// Cancellation after Create reached the local server; the caller may fail
	// while reading the reply, before UID binding. Not lost-COMMIT injection.
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	c.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) { cancel(); return false, nil, nil })
	s, err := newNamespacedFor(t, j, c)
	sandboxOK(t, err)
	intent := sandboxIntent()
	id, uid, err := s.Allocate(call, intent)
	if err == nil || uid != "" {
		t.Fatal("UID persistence/cancellation exposed usable pod")
	}
	v, err := j.ReadExact(ctx, id)
	sandboxOK(t, err)
	if v.CreateClaim == nil || v.Released {
		t.Fatal("lost unresolved obligation")
	}
	sandboxCount(t, pool, target, 1)
	_, _, err = s.Allocate(ctx, intent)
	sandboxError(t, err, gates.ErrSandboxClaimed)
}
