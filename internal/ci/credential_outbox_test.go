package ci_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/ci"
)

func TestCredentialTerminalOutboxSurvivesDeletion(t *testing.T) {
	p := ciPool(t)
	ctx := context.Background()
	s := ci.NewStore(p)
	for _, transition := range []string{"success", "failure", "cancelled", "delete-job", "delete-run"} {
		t.Run(transition, func(t *testing.T) {
			org := uuid.New()
			run, _, err := s.CreateRun(ctx, ci.Run{OrgID: org, RepoID: uuid.New(), CommitSHA: uuid.NewString(), Ref: "main"})
			if err != nil {
				t.Fatal(err)
			}
			job, err := s.CreateJob(ctx, ci.WorkflowJob{RunID: run.ID, Name: "credentials", RunCmd: "true", Secrets: []string{"TOKEN"}})
			if err != nil {
				t.Fatal(err)
			}
			switch transition {
			case "delete-job":
				_, err = p.Exec(ctx, `DELETE FROM ci.workflow_jobs WHERE id=$1`, job.ID)
			case "delete-run":
				err = s.DeleteRun(ctx, run.ID)
			default:
				err = s.SetJobStatus(ctx, job.ID, transition, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			var count int
			err = p.QueryRow(ctx, `SELECT count(*) FROM ci.credential_cleanup WHERE org_id=$1 AND job_id=$2 AND attempt_id=$3 AND phase='cleanup'`, org, job.ID, uuid.Nil).Scan(&count)
			if err != nil || count != 1 {
				t.Fatalf("terminal cleanup not durable: count=%d err=%v", count, err)
			}
			if transition != "delete-job" && transition != "delete-run" {
				if err = s.SetJobStatus(ctx, job.ID, "running", ""); err == nil {
					t.Fatal("terminal job resurrected")
				}
			}
		})
	}
}
