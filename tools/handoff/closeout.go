package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Session is an explicit approved-scope inventory, never an inferred ownership
// registry. The read-only checker does not authorize any remote/local mutation.
type Session struct {
	Version    int         `json:"version"`
	ID         string      `json:"id"`
	Objectives []Objective `json:"objectives"`
	Worktrees  []string    `json:"worktrees"`
	Branches   []string    `json:"branches"`
}

type Objective struct {
	ID           string           `json:"id"`
	Delivery     string           `json:"delivery"`
	PRs          []Delivery       `json:"prs"`
	Issue        int              `json:"issue,omitempty"`
	Spec         string           `json:"spec,omitempty"`
	Runs         []RunRequirement `json:"runs"`
	MainEvidence string           `json:"main_evidence,omitempty"`
}

type Delivery struct {
	Number int    `json:"number"`
	Head   string `json:"head"`
}
type RequiredStep struct {
	Job  string `json:"job"`
	Name string `json:"name"`
}
type RunRequirement struct {
	ID       int64          `json:"id"`
	Head     string         `json:"head"`
	Steps    []RequiredStep `json:"steps"`
	Evidence []string       `json:"evidence"`
}
type CloseoutReport struct {
	Session  string   `json:"session"`
	Phase    string   `json:"phase"`
	Complete bool     `json:"complete"`
	Pending  []string `json:"pending"`
}
type ghQuery func(context.Context, ...string) ([]byte, error)

func queryGH(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = repo
	return cmd.Output()
}

func (s Session) validate() error {
	if s.Version != 1 || strings.TrimSpace(s.ID) == "" || len(s.Objectives) == 0 {
		return errors.New("session requires version=1, id and explicit nonempty objectives")
	}
	seen := map[string]bool{}
	for _, o := range s.Objectives {
		if strings.TrimSpace(o.ID) == "" || seen[o.ID] || (o.Delivery != "submitted" && o.Delivery != "merged") || (len(o.PRs) == 0 && o.Spec == "" && len(o.Runs) == 0 && o.Issue == 0) {
			return fmt.Errorf("invalid/duplicate objective %q or missing delivery/scope", o.ID)
		}
		seen[o.ID] = true
		for _, p := range o.PRs {
			if p.Number < 1 || !validHex(p.Head, 40, 64) {
				return fmt.Errorf("objective %s requires positive PR number and exact head", o.ID)
			}
		}
		for _, r := range o.Runs {
			if r.ID < 1 || !validHex(r.Head, 40, 64) || len(r.Steps) == 0 {
				return fmt.Errorf("objective %s requires run ID, exact head and required executed steps", o.ID)
			}
			for _, step := range r.Steps {
				if step.Job == "" || step.Name == "" {
					return errors.New("required run step needs job and name")
				}
			}
			for _, p := range r.Evidence {
				if !filepath.IsAbs(p) {
					return errors.New("artifact evidence paths must be absolute scratch paths")
				}
			}
		}
		if o.Issue < 0 {
			return errors.New("issue number must be positive when present")
		}
		if o.Spec != "" && (!strings.HasPrefix(o.Spec, ".kiro/specs/archive/") || filepath.IsAbs(o.Spec) || strings.Contains(o.Spec, "..") || !strings.HasSuffix(o.Spec, "/spec.json")) {
			return errors.New("spec must name an archived repository-relative spec.json")
		}
		if o.MainEvidence != "" && !filepath.IsAbs(o.MainEvidence) {
			return errors.New("main evidence must be an absolute task artifact path")
		}
	}
	for _, p := range s.Worktrees {
		if !filepath.IsAbs(p) || !strings.Contains(filepath.ToSlash(p), "/worktrees/") {
			return errors.New("owned worktree paths must be explicit absolute paths under worktrees")
		}
	}
	for _, b := range s.Branches {
		if b == "" || b == "main" || b == "dev" || strings.HasPrefix(b, "refs/") || strings.ContainsAny(b, "*?[] ") {
			return errors.New("owned branches must be exact task-local branch names")
		}
	}
	return nil
}

func checkSession(ctx context.Context, repo string, s Session, phase string, query ghQuery) (CloseoutReport, error) {
	report := CloseoutReport{Session: s.ID, Phase: phase, Pending: []string{}}
	if err := s.validate(); err != nil {
		return report, err
	}
	if phase != "complete" && phase != "submitted" {
		return report, errors.New("closeout phase must be submitted or complete")
	}
	for _, o := range s.Objectives {
		add := func(message string) { report.Pending = append(report.Pending, o.ID+": "+message) }
		for _, p := range o.PRs {
			var data struct {
				State, HeadRefOid string
				MergeCommit       *struct{ Oid string }
			}
			raw, err := query(ctx, "pr", "view", strconv.Itoa(p.Number), "--json", "state,headRefOid,mergeCommit")
			if err != nil || json.Unmarshal(raw, &data) != nil {
				add(fmt.Sprintf("PR %d unavailable", p.Number))
				continue
			}
			if data.HeadRefOid != p.Head {
				add(fmt.Sprintf("PR %d head changed; rebind evidence", p.Number))
			}
			if o.Delivery == "merged" && data.State != "MERGED" {
				add(fmt.Sprintf("PR %d not merged", p.Number))
			}
			if data.State != "OPEN" && data.State != "MERGED" {
				add(fmt.Sprintf("PR %d closed without delivery", p.Number))
			}
		}
		for _, r := range o.Runs {
			var data struct {
				HeadSha, Status, Conclusion string
				Jobs                        []struct {
					Name, Conclusion string
					Steps            []struct{ Name, Conclusion string }
				}
			}
			raw, err := query(ctx, "run", "view", strconv.FormatInt(r.ID, 10), "--json", "headSha,status,conclusion,jobs")
			if err != nil || json.Unmarshal(raw, &data) != nil {
				add(fmt.Sprintf("required run %d unavailable", r.ID))
				continue
			}
			if data.HeadSha != r.Head || data.Status != "completed" || data.Conclusion != "success" {
				add(fmt.Sprintf("required run %d pending, failed or stale", r.ID))
			}
			for _, required := range r.Steps {
				passed := false
				for _, job := range data.Jobs {
					if job.Name != required.Job || job.Conclusion != "success" {
						continue
					}
					for _, step := range job.Steps {
						if step.Name == required.Name && step.Conclusion == "success" {
							passed = true
						}
					}
				}
				if !passed {
					add(fmt.Sprintf("run %d required step %s/%s not executed successfully", r.ID, required.Job, required.Name))
				}
			}
			for _, p := range r.Evidence {
				bytes, err := os.ReadFile(p)
				if err != nil || !artifactSHA(string(bytes), r.Head) {
					add("required artifact missing or wrong tested_sha: " + p)
				}
			}
		}
		if phase == "submitted" {
			continue
		}
		if o.Delivery == "merged" && len(o.PRs) != 0 && o.MainEvidence == "" {
			add("merged-main verification artifact required")
		}
		if o.Issue != 0 {
			var data struct{ State string }
			raw, err := query(ctx, "issue", "view", strconv.Itoa(o.Issue), "--json", "state")
			if err != nil || json.Unmarshal(raw, &data) != nil || data.State != "CLOSED" {
				add(fmt.Sprintf("issue %d not closed/verified", o.Issue))
			}
		}
		if o.Spec != "" {
			var data struct {
				Phase     string
				Completed bool
				Ready     *bool `json:"ready_for_implementation"`
			}
			raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(o.Spec)))
			if err != nil || json.Unmarshal(raw, &data) != nil || !data.Completed || data.Ready == nil || *data.Ready || (data.Phase != "completed" && data.Phase != "superseded") {
				add("spec is not completely archived: " + o.Spec)
			}
			active := strings.Replace(o.Spec, ".kiro/specs/archive/", ".kiro/specs/", 1)
			if _, err := os.Lstat(filepath.Dir(filepath.Join(repo, active))); !errors.Is(err, os.ErrNotExist) {
				add("active spec directory still exists or cannot be checked")
			}
		}
		if o.MainEvidence != "" {
			file, err := os.Open(o.MainEvidence)
			if err != nil {
				add("merged-main evidence unavailable")
				continue
			}
			result, decodeErr := decodeResult(file)
			closeErr := file.Close()
			if errors.Join(decodeErr, closeErr) != nil || result.Validate(filepath.Dir(o.MainEvidence)) != nil || (result.Status != "READY_FOR_REVIEW" && result.Status != "APPROVED") {
				add("merged-main evidence invalid")
			}
			for _, p := range o.PRs {
				var data struct{ MergeCommit *struct{ Oid string } }
				raw, err := query(ctx, "pr", "view", strconv.Itoa(p.Number), "--json", "mergeCommit")
				if err != nil || json.Unmarshal(raw, &data) != nil || data.MergeCommit == nil {
					add("merged-main evidence lacks merge provenance")
					continue
				}
				if _, err := sourceGit(ctx, repo, "merge-base", "--is-ancestor", data.MergeCommit.Oid, result.Source.Head); err != nil {
					add("tested main revision does not contain declared merge")
				}
			}
		}
	}
	if phase == "complete" {
		if len(s.Worktrees) != 0 {
			listing, err := sourceGit(ctx, repo, "worktree", "list", "--porcelain")
			if err != nil {
				return report, err
			}
			for _, p := range s.Worktrees {
				_, err := os.Lstat(p)
				if !errors.Is(err, os.ErrNotExist) || strings.Contains(string(listing), "worktree "+filepath.Clean(p)+"\n") {
					report.Pending = append(report.Pending, "owned worktree remains or cannot be checked: "+p)
				}
			}
		}
		for _, b := range s.Branches {
			out, err := sourceGit(ctx, repo, "for-each-ref", "--format=%(refname)", "refs/heads/"+b)
			if err != nil {
				return report, err
			}
			for _, line := range strings.Fields(string(out)) {
				if line == "refs/heads/"+b {
					report.Pending = append(report.Pending, "owned local branch remains: "+b)
				}
			}
		}
	}
	report.Complete = phase == "complete" && len(report.Pending) == 0
	return report, nil
}

func artifactSHA(text, sha string) bool {
	matched := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "tested_sha=") {
			if strings.TrimPrefix(line, "tested_sha=") != sha {
				return false
			}
			matched = true
		}
	}
	return matched
}
