/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v60/github"
)

// DiffPolicy bounds what an agent's PR is allowed to change. It is the primary
// control against the supply-chain pivot: even a fully prompt-injected agent
// cannot land changes the operator refuses to forward to a human reviewer.
//
// The path lists are deliberately kept in code (not a ConfigMap) because they
// are security-critical — making them casually editable cluster state would
// undermine the control. Only the numeric caps are env-overridable.
type DiffPolicy struct {
	// RestrictedPaths: any match fails the DevTask unconditionally.
	RestrictedPaths []string
	// RiskyPaths: any match fails the DevTask UNLESS the issue body contains
	// an "approve-risky-paths: <glob>" token whose glob matches the path.
	RiskyPaths []string
	MaxFiles   int
	MaxLines   int
}

// DiffViolation is one reason a PR is rejected. Reason is a stable machine tag;
// Detail is the human-readable explanation posted on the PR.
type DiffViolation struct {
	Reason string // "restricted-path" | "risky-path" | "too-many-files" | "too-many-lines"
	Path   string // populated for path violations
	Detail string
}

// defaultDiffPolicy is the shipped baseline. Numeric caps are starting points;
// tune from real demo data, not gut feel.
func defaultDiffPolicy() DiffPolicy {
	return DiffPolicy{
		RestrictedPaths: []string{
			".github/**",
			".devcontainer/**",
			"Dockerfile",
			".mcp.json",
			"operator/**", // the operator must not modify itself
			"deploy/**",
		},
		// Risky paths are matched at ANY depth: build/install manifests execute
		// code during npm install / make / docker build / pip install / etc., so
		// a nested one (services/api/package.json) is as dangerous as a top-level
		// one. Keep top-level and **/ variants in lockstep — an asymmetry here is
		// a bypass.
		RiskyPaths: []string{
			"package.json", "**/package.json",
			"package-lock.json", "**/package-lock.json",
			"yarn.lock", "**/yarn.lock",
			"pnpm-lock.yaml", "**/pnpm-lock.yaml",
			"**/Dockerfile", // top-level Dockerfile is restricted (checked first)
			"Makefile", "**/Makefile",
			"pyproject.toml", "**/pyproject.toml",
			"setup.py", "**/setup.py",
			"Gemfile", "**/Gemfile",
			"Cargo.toml", "**/Cargo.toml",
			"go.mod", "**/go.mod",
		},
		MaxFiles: 25,
		MaxLines: 800,
	}
}

// loadDiffPolicy returns the default policy with MAX_FILES_CHANGED /
// MAX_LINES_CHANGED env overrides applied when set and valid.
func loadDiffPolicy() DiffPolicy {
	p := defaultDiffPolicy()
	if v := os.Getenv("MAX_FILES_CHANGED"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.MaxFiles = n
		}
	}
	if v := os.Getenv("MAX_LINES_CHANGED"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.MaxLines = n
		}
	}
	return p
}

// evaluateDiff returns every policy violation in the given PR file set. An empty
// slice means the PR passes.
func evaluateDiff(files []*gh.CommitFile, issueBody string, policy DiffPolicy) []DiffViolation {
	approved := parseApprovedGlobs(issueBody)
	var violations []DiffViolation

	totalLines := 0
	for _, f := range files {
		p := f.GetFilename()
		totalLines += f.GetAdditions() + f.GetDeletions()

		if pat, ok := matchAny(policy.RestrictedPaths, p); ok {
			violations = append(violations, DiffViolation{
				Reason: "restricted-path",
				Path:   p,
				Detail: fmt.Sprintf("%s is a restricted path (matched %q) and may never be modified by the agent", p, pat),
			})
			continue
		}
		if pat, ok := matchAny(policy.RiskyPaths, p); ok {
			if !matchedByAny(approved, p) {
				violations = append(violations, DiffViolation{
					Reason: "risky-path",
					Path:   p,
					Detail: fmt.Sprintf("%s is a risky path (matched %q); add 'approve-risky-paths: %s' to the issue body to allow it", p, pat, pat),
				})
			}
		}
	}

	if len(files) > policy.MaxFiles {
		violations = append(violations, DiffViolation{
			Reason: "too-many-files",
			Detail: fmt.Sprintf("PR changes %d files, exceeding the limit of %d", len(files), policy.MaxFiles),
		})
	}
	if totalLines > policy.MaxLines {
		violations = append(violations, DiffViolation{
			Reason: "too-many-lines",
			Detail: fmt.Sprintf("PR changes %d lines, exceeding the limit of %d", totalLines, policy.MaxLines),
		})
	}
	return violations
}

// parseApprovedGlobs extracts globs from "approve-risky-paths: <glob> [<glob>...]"
// lines in the issue body. Tokens are whitespace- or comma-separated.
func parseApprovedGlobs(issueBody string) []string {
	const marker = "approve-risky-paths:"
	var globs []string
	for _, line := range strings.Split(issueBody, "\n") {
		idx := strings.Index(strings.ToLower(line), marker)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(marker):]
		for _, tok := range strings.FieldsFunc(rest, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ',' || r == '`'
		}) {
			if tok != "" {
				globs = append(globs, tok)
			}
		}
	}
	return globs
}

func matchAny(patterns []string, p string) (string, bool) {
	for _, pat := range patterns {
		if globMatch(pat, p) {
			return pat, true
		}
	}
	return "", false
}

func matchedByAny(patterns []string, p string) bool {
	_, ok := matchAny(patterns, p)
	return ok
}

// globMatch supports three forms beyond exact match:
//   - "<prefix>/**" matches the prefix dir and anything under it.
//   - "**/<suffix>" matches <suffix> at any depth (suffix may itself contain a
//     single-segment "*", matched against the basename).
//   - a single-segment pattern with "*" is matched with path.Match.
//
// Paths use forward slashes (GitHub's filename format).
func globMatch(pattern, p string) bool {
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		base := path.Base(p)
		if strings.Contains(suffix, "*") {
			if ok, _ := path.Match(suffix, base); ok {
				return true
			}
		}
		return p == suffix || strings.HasSuffix(p, "/"+suffix)
	}
	if strings.Contains(pattern, "*") {
		ok, _ := path.Match(pattern, p)
		return ok
	}
	return p == pattern
}
