package deployment

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/novaforge/novaforge/internal/approvals"
	"k8s.io/client-go/kubernetes"
)

type Config struct {
	Targets []TargetConfig `json:"targets"`
}
type TargetConfig struct {
	Name           string     `json:"name"`
	CredentialName string     `json:"credential_name"`
	OrgID          uuid.UUID  `json:"org_id"`
	RepoID         uuid.UUID  `json:"repo_id"`
	Environment    string     `json:"environment"`
	Helm           HelmConfig `json:"helm"`
}
type Dependencies struct {
	ResolveRun      RunResolver
	Kubernetes      kubernetes.Interface
	Credentials     CredentialProvisioner
	CredentialOwner DeploymentCredentialOwner
	HMACSecret      string
}

// NewConfiguredService consumes operator metadata only. A blank path disables
// deployment explicitly; malformed or incomplete configuration fails startup.
// The injected Kubernetes client controls executor Jobs, never target releases.
func NewConfiguredService(pool *pgxpool.Pool, store *approvals.Store, configPath string, deps Dependencies) (*Service, error) {
	if configPath == "" {
		return nil, nil
	}
	file, err := os.Open(configPath)
	if err != nil {
		return nil, errors.New("deployment configuration cannot be read")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("deployment configuration exceeds read limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, errors.New("invalid deployment configuration")
	}
	if decoder.Decode(new(any)) != io.EOF || len(cfg.Targets) == 0 {
		return nil, errors.New("deployment configuration requires targets and one JSON object")
	}
	if deps.Credentials != nil && deps.CredentialOwner != nil {
		return nil, errors.New("deployment credential providers are mutually exclusive")
	}
	var targets []Target
	for _, c := range cfg.Targets {
		credentials := deps.Credentials
		if deps.CredentialOwner != nil {
			// Revision binds the credential name as well as fixed Helm policy.
			c.Helm.CredentialName = c.CredentialName
			metadata := &HelmExecutor{config: c.Helm}
			var err error
			credentials, err = NewSecretMaterializer(pool, deps.Kubernetes, deps.CredentialOwner, deps.HMACSecret, []CredentialBinding{{Target: Target{Name: c.Name, OrgID: c.OrgID, RepoID: c.RepoID, Environment: c.Environment, Destination: metadata.Destination(), Revision: metadata.Revision()}, Namespace: c.Helm.ExecutionNamespace, Name: c.CredentialName}})
			if err != nil {
				return nil, err
			}
		}
		executor, err := NewHelmExecutor(deps.Kubernetes, c.Helm, credentials)
		if err != nil {
			return nil, err
		}
		targets = append(targets, Target{Name: c.Name, OrgID: c.OrgID, RepoID: c.RepoID, Environment: c.Environment, Destination: executor.Destination(), Revision: executor.Revision(), Executor: executor})
	}
	return NewService(pool, store, targets, deps.ResolveRun)
}
