package gitops

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/novaforge/novaforge/internal/authz"
)

// FingerprintFunc resolves an SSH public key's SHA256 fingerprint to the
// caller's authorization scope, or an error if the fingerprint is not
// registered to anyone.
type FingerprintFunc func(ctx context.Context, fingerprint string) (authz.Scope, error)

// gitCommandRe matches the exec payload git sends for smart-SSH transport:
// git-upload-pack '<org>/<repo>.git' or git-receive-pack '<org>/<repo>.git'.
// It is anchored on both ends so nothing outside these two commands, quoted
// exactly this way, is accepted.
var gitCommandRe = regexp.MustCompile(`^git-(upload|receive)-pack '([^']+)'$`)

// SSHServer serves the git smart-SSH transport: it accepts only
// git-upload-pack and git-receive-pack exec requests, authenticating by
// public key fingerprint and authorizing pushes exactly as the HTTP
// transport does, so both transports refuse an out-of-scope ref identically.
type SSHServer struct {
	root      string
	hostKey   ssh.Signer
	lookup    FingerprintFunc
	caps      CapFunc
	sshConfig *ssh.ServerConfig

	listener net.Listener
}

// NewSSHServer returns an SSHServer rooted at root, presenting hostKey to
// clients, resolving client public keys via lookup, and authorizing pushes
// via caps.
func NewSSHServer(root string, hostKey ssh.Signer, lookup FingerprintFunc, caps CapFunc) *SSHServer {
	s := &SSHServer{root: root, hostKey: hostKey, lookup: lookup, caps: caps}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			fp := ssh.FingerprintSHA256(key)
			scope, err := lookup(context.Background(), fp)
			if err != nil {
				return nil, fmt.Errorf("permission denied")
			}
			return &ssh.Permissions{
				Extensions: map[string]string{
					"org_id":     scope.OrgID.String(),
					"actor_id":   scope.ActorID.String(),
					"actor_kind": scope.ActorKind,
				},
			}, nil
		},
	}
	config.AddHostKey(hostKey)
	s.sshConfig = config
	return s
}

// Addr returns the address the server is listening on, once Serve has been
// called. It returns the empty string before that.
func (s *SSHServer) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Serve accepts connections on l until it is closed, handling each
// connection's git-upload-pack / git-receive-pack requests. It always
// returns a non-nil error, matching net.Listener.Accept's contract.
func (s *SSHServer) Serve(l net.Listener) error {
	s.listener = l
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *SSHServer) handleConn(nc net.Conn) {
	defer nc.Close()

	sconn, chans, reqs, err := ssh.NewServerConn(nc, s.sshConfig)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(sconn, channel, requests)
	}
}

// execRequest is the payload of an "exec" channel request, per RFC 4254 6.5.
type execRequest struct {
	Command string
}

func (s *SSHServer) handleSession(sconn *ssh.ServerConn, channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()

	for req := range requests {
		if req.Type != "exec" {
			if req.WantReply {
				req.Reply(false, nil)
			}
			continue
		}

		var payload execRequest
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			if req.WantReply {
				req.Reply(false, nil)
			}
			continue
		}
		if req.WantReply {
			req.Reply(true, nil)
		}

		s.runGitCommand(sconn, channel, payload.Command)
		return
	}
}

func (s *SSHServer) runGitCommand(sconn *ssh.ServerConn, channel ssh.Channel, command string) {
	m := gitCommandRe.FindStringSubmatch(command)
	if m == nil {
		fmt.Fprintf(channel.Stderr(), "unsupported command\n")
		writeExitStatus(channel, 128)
		return
	}
	service := "git-" + m[1] + "-pack"
	repoPath := strings.TrimPrefix(m[2], "/")

	parts := strings.SplitN(repoPath, "/", 2)
	if len(parts) != 2 || !strings.HasSuffix(parts[1], ".git") {
		fmt.Fprintf(channel.Stderr(), "malformed repository path %q\n", repoPath)
		writeExitStatus(channel, 128)
		return
	}
	orgID, err := uuid.Parse(parts[0])
	if err != nil {
		fmt.Fprintf(channel.Stderr(), "invalid org id %q\n", parts[0])
		writeExitStatus(channel, 128)
		return
	}
	repoName := strings.TrimSuffix(parts[1], ".git")

	path, err := resolvePath(s.root, orgID, repoName)
	if err != nil {
		fmt.Fprintf(channel.Stderr(), "%v\n", err)
		writeExitStatus(channel, 128)
		return
	}

	ext := sconn.Permissions.Extensions
	scope := authz.Scope{ActorKind: ext["actor_kind"]}
	scope.OrgID, _ = uuid.Parse(ext["org_id"])
	scope.ActorID, _ = uuid.Parse(ext["actor_id"])

	if err := authz.RequireOrg(authz.WithScope(context.Background(), scope), orgID); err != nil {
		fmt.Fprintf(channel.Stderr(), "%v\n", err)
		writeExitStatus(channel, 1)
		return
	}

	if service == "git-receive-pack" {
		// Unlike HTTP, where the client's request only starts after it has
		// already received the ref advertisement from a prior GET
		// info/refs, the interactive SSH client is waiting for this server
		// to speak first: it will not send any push data until it has read
		// the ref advertisement. So the advertisement is sent before
		// reading anything, and only then is the full request read into
		// memory and its ref updates parsed and checked against caps,
		// exactly as the HTTP transport does, before any git process
		// capable of touching the repository starts.
		adv, err := exec.Command("git", "receive-pack", "--stateless-rpc", "--advertise-refs", path).Output()
		if err != nil {
			fmt.Fprintf(channel.Stderr(), "git receive-pack --advertise-refs: %v\n", err)
			writeExitStatus(channel, 1)
			return
		}
		if _, err := channel.Write(adv); err != nil {
			return
		}

		body, err := io.ReadAll(channel)
		if err != nil {
			writeExitStatus(channel, 1)
			return
		}
		refs := parseReceivePackRefs(body)
		if err := s.caps(context.Background(), scope, orgID, repoName, refs); err != nil {
			s.rejectSSHPush(channel, refs, err)
			return
		}
		s.runGit(channel, path, service, bytes.NewReader(body), scope, orgID, repoName, body, true)
		return
	}

	s.runGit(channel, path, service, channel, scope, orgID, repoName, nil, false)
}

func (s *SSHServer) runGit(channel ssh.Channel, path, service string, stdin io.Reader, scope authz.Scope, orgID uuid.UUID, repoName string, body []byte, statelessRPC bool) {
	// git-upload-pack runs in its ordinary interactive protocol mode, since
	// SSH is a single persistent connection and upload-pack speaks its
	// advertisement and negotiation directly over the channel. git-receive-
	// pack instead runs --stateless-rpc here because this server already
	// sent its own advertisement separately (see runGitCommand) in order to
	// read and authorize the push before running git at all; --stateless-
	// rpc skips generating a second advertisement that the client isn't
	// expecting.
	args := []string{service[len("git-"):]}
	if statelessRPC {
		args = append(args, "--stateless-rpc")
	}
	args = append(args, path)
	cmd := exec.Command("git", args...)
	cmd.Stdin = stdin
	var stdout bytes.Buffer
	cmd.Stdout = io.MultiWriter(channel, &stdout)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(channel.Stderr(), "git %s: %v — %s\n", service, err, stderr.String())
		writeExitStatus(channel, 1)
		return
	}
	writeExitStatus(channel, 0)

	if service == "git-receive-pack" && receivePackSucceeded(stdout.Bytes()) && PushPublisher != nil {
		updates := parseReceivePackRefUpdates(body)
		PushPublisher(context.Background(), scope, orgID, repoName, updates)
	}
}

func (s *SSHServer) rejectSSHPush(channel ssh.Channel, refs []string, err error) {
	channel.Write(rejectedReceivePackReport(refs, err))
	writeExitStatus(channel, 1)
}

func writeExitStatus(channel ssh.Channel, code uint32) {
	payload := struct{ Status uint32 }{code}
	channel.SendRequest("exit-status", false, ssh.Marshal(&payload))
	channel.Close()
}
