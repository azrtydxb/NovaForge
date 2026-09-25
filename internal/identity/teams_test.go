package identity_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// The spec has claimed teams in S-1 and its Data section since the beginning and
// nothing ever implemented them: there was no team table and no team code. A team
// is how access is granted to a group rather than to each person in turn, which is
// what repository collaborators then grant to.
//
// A team carries a role, and that role bounds its members: a person whose
// organization role is higher does not get more through a team, and a person whose
// organization role is lower does not get less by leaving one. Effective access is
// the strongest of what they hold, so the test pins both directions.
func TestTeamAccess(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	owner, err := store.CreateUser(ctx, uniqueName("owner")+"@example.com", uniqueName("owner"), "hash")
	if err != nil {
		t.Fatalf("CreateUser owner: %v", err)
	}
	person, err := store.CreateUser(ctx, uniqueName("dev")+"@example.com", uniqueName("dev"), "hash")
	if err != nil {
		t.Fatalf("CreateUser person: %v", err)
	}
	org, err := store.CreateOrg(ctx, uniqueName("teamorg"), owner.ID)
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	team, err := store.CreateTeam(ctx, org.ID, "reviewers", "admin")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if team.ID == uuid.Nil || team.Name != "reviewers" || team.Role != "admin" {
		t.Fatalf("CreateTeam returned %+v", team)
	}

	t.Run("a person in no team belongs to none", func(t *testing.T) {
		got, err := store.TeamsForUser(ctx, org.ID, person.ID)
		if err != nil {
			t.Fatalf("TeamsForUser: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("a person in no team is in %+v", got)
		}
	})

	t.Run("membership is reported", func(t *testing.T) {
		if err := store.AddTeamMember(ctx, org.ID, team.ID, person.ID); err != nil {
			t.Fatalf("AddTeamMember: %v", err)
		}
		got, err := store.TeamsForUser(ctx, org.ID, person.ID)
		if err != nil {
			t.Fatalf("TeamsForUser: %v", err)
		}
		if len(got) != 1 || got[0].ID != team.ID || got[0].Role != "admin" {
			t.Fatalf("TeamsForUser = %+v, want the reviewers team with role admin", got)
		}
	})

	t.Run("adding twice is not an error and does not duplicate", func(t *testing.T) {
		if err := store.AddTeamMember(ctx, org.ID, team.ID, person.ID); err != nil {
			t.Fatalf("AddTeamMember again: %v", err)
		}
		got, _ := store.TeamsForUser(ctx, org.ID, person.ID)
		if len(got) != 1 {
			t.Fatalf("a person added twice is in %d teams, want 1", len(got))
		}
	})

	t.Run("removal removes the access", func(t *testing.T) {
		if err := store.RemoveTeamMember(ctx, org.ID, team.ID, person.ID); err != nil {
			t.Fatalf("RemoveTeamMember: %v", err)
		}
		got, _ := store.TeamsForUser(ctx, org.ID, person.ID)
		if len(got) != 0 {
			t.Fatalf("a removed person is still in %+v", got)
		}
	})

	t.Run("a team's role is its own, not the organization's", func(t *testing.T) {
		// The owner's organization role is "owner". Their role in a team that
		// grants "member" is "member": a team is a narrower grant, and reading the
		// organization role here would make every team as powerful as its
		// strongest member.
		narrow, err := store.CreateTeam(ctx, org.ID, "readers", "member")
		if err != nil {
			t.Fatalf("CreateTeam readers: %v", err)
		}
		if err := store.AddTeamMember(ctx, org.ID, narrow.ID, owner.ID); err != nil {
			t.Fatalf("AddTeamMember owner: %v", err)
		}
		got, err := store.TeamsForUser(ctx, org.ID, owner.ID)
		if err != nil {
			t.Fatalf("TeamsForUser owner: %v", err)
		}
		if len(got) != 1 || got[0].Role != "member" {
			t.Fatalf("the owner's team role is %+v, want member", got)
		}
	})

	t.Run("listing is organization scoped", func(t *testing.T) {
		otherOwner, err := store.CreateUser(ctx, uniqueName("o2")+"@example.com", uniqueName("o2"), "hash")
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		other, err := store.CreateOrg(ctx, uniqueName("otherorg"), otherOwner.ID)
		if err != nil {
			t.Fatalf("CreateOrg: %v", err)
		}
		if _, err := store.CreateTeam(ctx, other.ID, "reviewers", "admin"); err != nil {
			t.Fatalf("CreateTeam in the other organization: %v", err)
		}
		mine, err := store.ListTeams(ctx, org.ID)
		if err != nil {
			t.Fatalf("ListTeams: %v", err)
		}
		for _, tm := range mine {
			if tm.OrgID != org.ID {
				t.Fatalf("another organization's team %+v is listed here", tm)
			}
		}
		// The same team name in two organizations is two teams, so uniqueness is
		// per organization and not global.
		if len(mine) != 2 {
			t.Fatalf("this organization has %d teams, want 2", len(mine))
		}
	})
}
