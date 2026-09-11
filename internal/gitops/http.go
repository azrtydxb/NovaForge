package gitops

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/novaforge/novaforge/internal/authz"
)

// AuthFunc authenticates HTTP Basic credentials and returns the caller's
// authorization scope.
// orgRef is the organization as it appears in the clone URL: a human-typed
// name or an id. Resolving it is identity's job, so AuthFunc returns a scope
// whose OrgID is the resolved organization — the transport never trusts a
// path segment as an identifier on its own.
type AuthFunc func(ctx context.Context, user, pass, orgRef string) (authz.Scope, error)

// CapFunc authorizes a set of ref updates (for git-receive-pack) against a
// scope, before any git process is spawned. It is called with an empty refs
// slice for git-upload-pack, where it may still deny read access.
type CapFunc func(ctx context.Context, s authz.Scope, orgID uuid.UUID, repo string, refs []string) error

type httpHandler struct {
	root string
	auth AuthFunc
	caps CapFunc
}

// NewHTTPHandler returns an http.Handler serving the git smart-HTTP protocol
// under /{org}/{repo}.git/... rooted at root. Every request is authenticated
// via auth and, for git-receive-pack, every requested ref update is checked
// via caps before the git process starts.
func NewHTTPHandler(root string, auth AuthFunc, caps CapFunc) http.Handler {
	return &httpHandler{root: root, auth: auth, caps: caps}
}

// parsedPath is the {org}/{repo}.git/{op} decomposition of a request path.
type parsedPath struct {
	orgRef string
	repo   string
	op     string // "info/refs", "git-upload-pack", or "git-receive-pack"
}

func parsePath(p string) (parsedPath, error) {
	p = strings.TrimPrefix(p, "/")
	parts := strings.SplitN(p, "/", 3)
	if len(parts) != 3 {
		return parsedPath{}, fmt.Errorf("malformed path %q", p)
	}
	orgRef := parts[0]
	if orgRef == "" {
		return parsedPath{}, fmt.Errorf("missing organization in path")
	}
	repoPart := parts[1]
	if !strings.HasSuffix(repoPart, ".git") {
		return parsedPath{}, fmt.Errorf("malformed repository path %q", repoPart)
	}
	repo := strings.TrimSuffix(repoPart, ".git")
	return parsedPath{orgRef: orgRef, repo: repo, op: parts[2]}, nil
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pp, err := parsePath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	user, pass, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="novaforge"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	// The organization from the clone URL is resolved and membership-checked
	// during authentication, so the scope that comes back is already the
	// caller's scope in that organization.
	scope, err := h.auth(r.Context(), user, pass, pp.orgRef)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="novaforge"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if scope.OrgID == uuid.Nil {
		http.Error(w, "no organization scope for "+pp.orgRef, http.StatusForbidden)
		return
	}

	path, err := resolvePath(h.root, scope.OrgID, pp.repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	switch {
	case pp.op == "info/refs" && r.Method == http.MethodGet:
		h.infoRefs(w, r, scope, pp, path)
	case pp.op == "git-upload-pack" && r.Method == http.MethodPost:
		h.rpc(w, r, scope, pp, path, "git-upload-pack")
	case pp.op == "git-receive-pack" && r.Method == http.MethodPost:
		h.rpc(w, r, scope, pp, path, "git-receive-pack")
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (h *httpHandler) infoRefs(w http.ResponseWriter, r *http.Request, scope authz.Scope, pp parsedPath, path string) {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" && service != "git-receive-pack" {
		http.Error(w, "unsupported service", http.StatusBadRequest)
		return
	}
	if service == "git-receive-pack" {
		if err := h.caps(r.Context(), scope, scope.OrgID, pp.repo, nil); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	cmd := exec.Command("git", service[len("git-"):], "--stateless-rpc", "--advertise-refs", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		http.Error(w, fmt.Sprintf("git %s --advertise-refs: %v — %s", service, err, stderr.String()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-advertisement", service))
	w.WriteHeader(http.StatusOK)
	writePktLine(w, fmt.Sprintf("# service=%s\n", service))
	w.Write([]byte("0000"))
	w.Write(out)
}

func (h *httpHandler) rpc(w http.ResponseWriter, r *http.Request, scope authz.Scope, pp parsedPath, path, service string) {
	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if service == "git-receive-pack" {
		refs := parseReceivePackRefs(body)
		if err := h.caps(r.Context(), scope, scope.OrgID, pp.repo, refs); err != nil {
			h.rejectPush(w, refs, err)
			return
		}
	}

	cmd := exec.Command("git", service[len("git-"):], "--stateless-rpc", path)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		http.Error(w, fmt.Sprintf("git %s: %v — %s", service, err, stderr.String()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-result", service))
	w.WriteHeader(http.StatusOK)
	w.Write(stdout.Bytes())

	if service == "git-receive-pack" && receivePackSucceeded(stdout.Bytes()) {
		if PushPublisher != nil {
			updates := parseReceivePackRefUpdates(body)
			PushPublisher(r.Context(), scope, scope.OrgID, pp.repo, updates)
		}
	}
}

// rejectPush denies a git-receive-pack request that failed a capability
// check. A bare HTTP error status is not enough here: git's smart-HTTP
// client discards the response body for non-2xx statuses, so the denial
// reason would never reach the user. Instead this responds 200 with a
// synthesized receive-pack report-status body marking every requested ref
// "ng" (not ok) with err's message as the reason, which git surfaces as a
// "[remote rejected]" line carrying that reason.
func (h *httpHandler) rejectPush(w http.ResponseWriter, refs []string, err error) {
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.WriteHeader(http.StatusOK)
	w.Write(rejectedReceivePackReport(refs, err))
}

// rejectedReceivePackReport builds a synthesized git-receive-pack
// report-status response marking every ref in refs "ng" (not ok) with err's
// message as the reason, wrapped in side-band-64k framing. Both the HTTP and
// SSH transports negotiate side-band-64k during their ref advertisement, so
// both must wrap this identically: an unwrapped report-status is misread by
// the client as a corrupt sideband packet, and a bare error status code
// never reaches the user at all, since git's client discards the response
// body for non-2xx statuses.
func rejectedReceivePackReport(refs []string, err error) []byte {
	// The report-status body is itself a pkt-line stream: a status line,
	// one ok/ng line per ref, and a flush-pkt.
	var status bytes.Buffer
	writePktLine(&status, "unpack ok\n")
	for _, ref := range refs {
		writePktLine(&status, fmt.Sprintf("ng %s %s\n", ref, err.Error()))
	}
	status.Write([]byte("0000"))

	// The whole report-status stream above is carried as the payload of a
	// single outer pkt-line prefixed with the band byte (1 = primary data),
	// followed by the outer flush-pkt.
	var out bytes.Buffer
	writePktLine(&out, "\x01"+status.String())
	out.Write([]byte("0000"))
	return out.Bytes()
}

// RefUpdate is one ref update requested by a push.
type RefUpdate struct {
	OldSHA string
	NewSHA string
	Ref    string
}

// PushPublisher, when non-nil, is invoked after a git-receive-pack request
// succeeds (both over HTTP and over SSH), once per push, with every ref
// update the push applied. It is nil by default so gitops has no hard
// dependency on Redis; the git-platform service assigns it at startup to
// publish events.PushEvent on the push stream.
var PushPublisher func(ctx context.Context, scope authz.Scope, orgID uuid.UUID, repo string, updates []RefUpdate)

func readBody(r *http.Request) ([]byte, error) {
	var reader io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("decompress request body: %w", err)
		}
		defer gz.Close()
		reader = gz
	}
	return io.ReadAll(reader)
}

func writePktLine(w io.Writer, s string) {
	n := len(s) + 4
	fmt.Fprintf(w, "%04x%s", n, s)
}

// parseReceivePackRefs extracts the ref names requested for update from a
// raw git-receive-pack request body, without invoking git. This lets the
// server enforce capability checks before any git process starts.
func parseReceivePackRefs(body []byte) []string {
	updates := parseReceivePackRefUpdates(body)
	refs := make([]string, 0, len(updates))
	for _, u := range updates {
		refs = append(refs, u.Ref)
	}
	return refs
}

func parseReceivePackRefUpdates(body []byte) []RefUpdate {
	var updates []RefUpdate
	i := 0
	for i+4 <= len(body) {
		lenHex := string(body[i : i+4])
		n, err := strconv.ParseInt(lenHex, 16, 32)
		if err != nil {
			break
		}
		if n == 0 {
			// flush-pkt: end of ref update list.
			break
		}
		if i+int(n) > len(body) || n < 4 {
			break
		}
		payload := body[i+4 : i+int(n)]
		i += int(n)

		line := payload
		if nul := bytes.IndexByte(line, 0); nul >= 0 {
			line = line[:nul]
		}
		line = bytes.TrimRight(line, "\n")
		fields := strings.SplitN(string(line), " ", 3)
		if len(fields) != 3 {
			continue
		}
		updates = append(updates, RefUpdate{OldSHA: fields[0], NewSHA: fields[1], Ref: fields[2]})
	}
	return updates
}

// receivePackSucceeded reports whether a git-receive-pack response's
// pkt-line report-status indicates success ("unpack ok").
func receivePackSucceeded(out []byte) bool {
	return bytes.Contains(out, []byte("unpack ok"))
}
