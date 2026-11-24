// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PostStewardNote is a separate operator-selected write. A repeat reconciles
// the authenticated author's marker before creating a comment.
func PostStewardNote(ctx context.Context, id string, receipt PublicationReceipt) error {
	unlock, err := attemptLock(id)
	if err != nil {
		return err
	}
	defer unlock()
	a, err := Get(id)
	if err != nil {
		return err
	}
	st := a.Steward
	if st == nil || receipt.Commit != a.CandidateSHA || receipt.Previous != st.PreviousHead || receipt.Branch != "bugsnitch/"+st.RootAttempt {
		return errors.New("note requires a published correction")
	}
	pr, err := readStewardPR(ctx, a.Repository, st.Pull)
	if err != nil {
		return err
	}
	if !pr.matches(a.Repository, receipt.Branch, receipt.Commit, st.Target) {
		return errors.New("PR changed before recording the publication note")
	}
	marker := "<!-- bugsnitch:steward:" + a.ID + ":" + a.CandidateSHA + " -->"
	owner, name, _ := strings.Cut(a.Repository, "/")
	const notes = `query BugsnitchNotes($o:String!,$r:String!,$n:Int!,$before:String){repository(owner:$o,name:$r){pullRequest(number:$n){comments(last:50,before:$before){nodes{body viewerDidAuthor} pageInfo{hasPreviousPage startCursor}}}}}`
	before := ""
	for page := 0; page < 10; page++ {
		args := []string{"graphql", "-f", "query=" + notes, "-f", "o=" + owner, "-f", "r=" + name, "-F", fmt.Sprintf("n=%d", st.Pull)}
		if before != "" {
			args = append(args, "-f", "before="+before)
		}
		raw, err := githubCLI(ctx, args...)
		if err != nil {
			return err
		}
		var result struct {
			Errors []json.RawMessage
			Data   struct {
				Repository struct {
					PullRequest struct {
						Comments *struct {
							Nodes []struct {
								Body            string
								ViewerDidAuthor bool
							}
							PageInfo struct {
								HasPreviousPage bool
								StartCursor     string
							}
						}
					}
				}
			}
		}
		if json.Unmarshal(raw, &result) != nil || len(result.Errors) > 0 || result.Data.Repository.PullRequest.Comments == nil {
			return errors.New("could not reconcile existing PR notes")
		}
		comments := result.Data.Repository.PullRequest.Comments
		for _, c := range comments.Nodes {
			if c.ViewerDidAuthor && strings.HasSuffix(c.Body, marker) {
				return nil
			}
		}
		if !comments.PageInfo.HasPreviousPage {
			text := fmt.Sprintf("Bugsnitch published reviewed correction `%s` on `%s` after local validation and independent review. Previous revision: `%s`.\n\nTo undo the correction from the repository root on this branch:\n\n```sh\n%s\n```\n\n%s", receipt.Commit, receipt.Branch, receipt.Previous, receipt.Undo, marker)
			if CheckPublication != nil {
				if err := CheckPublication(ctx, subject(&a)); err != nil {
					return err
				}
			}
			_, err := githubCLI(ctx, "--method", "POST", fmt.Sprintf("repos/%s/issues/%d/comments", a.Repository, st.Pull), "-f", "body="+text)
			return err
		}
		if before == comments.PageInfo.StartCursor || comments.PageInfo.StartCursor == "" {
			break
		}
		before = comments.PageInfo.StartCursor
	}
	return errors.New("PR comment history could not be reconciled within the page limit")
}
