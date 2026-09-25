package capability

import (
	"fmt"
	"github.com/google/uuid"
	identityv1 "github.com/novaforge/novaforge/gen/novaforge/identity/v1"
	"time"
)

func GrantProto(g Grant) *identityv1.Grant {
	return &identityv1.Grant{Id: g.ID.String(), OrgId: g.OrgID.String(), SubjectId: g.SubjectID.String(), SubjectKind: g.SubjectKind, RepoRead: g.RepoRead, WriteBranch: g.WriteBranch, SecretsProd: g.SecretsProd, DeployStaging: g.DeployStaging, DeployProd: g.DeployProd, ExpiresAt: g.ExpiresAt.UTC().Format(time.RFC3339Nano)}
}
func GrantFromProto(g *identityv1.Grant) (Grant, error) {
	if g == nil {
		return Grant{}, fmt.Errorf("grant required")
	}
	id, e := uuid.Parse(g.Id)
	if e != nil || id == uuid.Nil {
		return Grant{}, fmt.Errorf("invalid grant id")
	}
	org, e := uuid.Parse(g.OrgId)
	if e != nil || org == uuid.Nil {
		return Grant{}, fmt.Errorf("invalid organization")
	}
	subject, e := uuid.Parse(g.SubjectId)
	if e != nil || subject == uuid.Nil {
		return Grant{}, fmt.Errorf("invalid subject")
	}
	expiry, e := time.Parse(time.RFC3339Nano, g.ExpiresAt)
	if e != nil {
		return Grant{}, fmt.Errorf("invalid expiry")
	}
	return Grant{ID: id, OrgID: org, SubjectID: subject, SubjectKind: g.SubjectKind, RepoRead: g.RepoRead, WriteBranch: g.WriteBranch, SecretsProd: g.SecretsProd, DeployStaging: g.DeployStaging, DeployProd: g.DeployProd, ExpiresAt: expiry}, nil
}
func IntentProto(i IssuanceIntent) *identityv1.GrantIntent {
	return &identityv1.GrantIntent{IssuerId: i.IssuerID.String(), IssuerKind: i.IssuerKind, RunId: i.RunID.String(), Grant: GrantProto(i.Grant)}
}
func IntentFromProto(p *identityv1.GrantIntent) (IssuanceIntent, error) {
	if p == nil {
		return IssuanceIntent{}, fmt.Errorf("intent required")
	}
	issuer, e := uuid.Parse(p.IssuerId)
	if e != nil || issuer == uuid.Nil {
		return IssuanceIntent{}, fmt.Errorf("invalid issuer")
	}
	run, e := uuid.Parse(p.RunId)
	if e != nil || run == uuid.Nil {
		return IssuanceIntent{}, fmt.Errorf("invalid run")
	}
	g, e := GrantFromProto(p.Grant)
	if e != nil {
		return IssuanceIntent{}, e
	}
	return IssuanceIntent{IssuerID: issuer, IssuerKind: p.IssuerKind, RunID: run, Grant: g}, nil
}
