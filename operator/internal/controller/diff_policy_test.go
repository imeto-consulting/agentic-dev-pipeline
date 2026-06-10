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
	"testing"

	gh "github.com/google/go-github/v60/github"
)

func file(name string, add, del int) *gh.CommitFile {
	return &gh.CommitFile{Filename: &name, Additions: &add, Deletions: &del}
}

func reasons(vs []DiffViolation) map[string]string {
	m := map[string]string{}
	for _, v := range vs {
		m[v.Reason] = v.Path
	}
	return m
}

func TestEvaluateDiff(t *testing.T) {
	policy := defaultDiffPolicy()

	tests := []struct {
		name      string
		files     []*gh.CommitFile
		issueBody string
		want      map[string]string // reason -> path (path "" for size violations)
	}{
		{
			name:  "clean docs change passes",
			files: []*gh.CommitFile{file("README.md", 10, 2), file("docs/guide.md", 5, 0)},
			want:  map[string]string{},
		},
		{
			name:  "github workflow is restricted",
			files: []*gh.CommitFile{file(".github/workflows/release.yml", 3, 1)},
			want:  map[string]string{"restricted-path": ".github/workflows/release.yml"},
		},
		{
			name:  "devcontainer is restricted",
			files: []*gh.CommitFile{file(".devcontainer/devcontainer.json", 2, 0)},
			want:  map[string]string{"restricted-path": ".devcontainer/devcontainer.json"},
		},
		{
			name:  "top-level Dockerfile is restricted",
			files: []*gh.CommitFile{file("Dockerfile", 1, 1)},
			want:  map[string]string{"restricted-path": "Dockerfile"},
		},
		{
			name:  "mcp config is restricted",
			files: []*gh.CommitFile{file(".mcp.json", 1, 0)},
			want:  map[string]string{"restricted-path": ".mcp.json"},
		},
		{
			name:  "operator self-edit is restricted",
			files: []*gh.CommitFile{file("operator/internal/controller/pod.go", 4, 0)},
			want:  map[string]string{"restricted-path": "operator/internal/controller/pod.go"},
		},
		{
			name:  "deploy dir is restricted",
			files: []*gh.CommitFile{file("deploy/triage/cronjob.yaml", 4, 0)},
			want:  map[string]string{"restricted-path": "deploy/triage/cronjob.yaml"},
		},
		{
			name:  "package.json is risky without approval",
			files: []*gh.CommitFile{file("package.json", 2, 1)},
			want:  map[string]string{"risky-path": "package.json"},
		},
		{
			name:      "package.json allowed with exact approve token",
			files:     []*gh.CommitFile{file("package.json", 2, 1)},
			issueBody: "Please bump the dep.\n\napprove-risky-paths: package.json",
			want:      map[string]string{},
		},
		{
			name:      "package.json allowed with glob approve token",
			files:     []*gh.CommitFile{file("package.json", 2, 1), file("package-lock.json", 40, 5)},
			issueBody: "approve-risky-paths: package*.json",
			want:      map[string]string{},
		},
		{
			name:      "approve token does not unlock restricted paths",
			files:     []*gh.CommitFile{file(".github/workflows/ci.yml", 1, 0)},
			issueBody: "approve-risky-paths: .github/**",
			want:      map[string]string{"restricted-path": ".github/workflows/ci.yml"},
		},
		{
			name:  "nested Dockerfile is risky",
			files: []*gh.CommitFile{file("services/api/Dockerfile", 2, 0)},
			want:  map[string]string{"risky-path": "services/api/Dockerfile"},
		},
		{
			name:  "nested package.json is risky (no top-level/nested asymmetry)",
			files: []*gh.CommitFile{file("services/api/package.json", 2, 0)},
			want:  map[string]string{"risky-path": "services/api/package.json"},
		},
		{
			name:  "nested pyproject.toml is risky",
			files: []*gh.CommitFile{file("pkg/pyproject.toml", 2, 0)},
			want:  map[string]string{"risky-path": "pkg/pyproject.toml"},
		},
		{
			name:  "too many files",
			files: manyFiles(26),
			want:  map[string]string{"too-many-files": ""},
		},
		{
			name:  "too many lines",
			files: []*gh.CommitFile{file("big.txt", 801, 0)},
			want:  map[string]string{"too-many-lines": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reasons(evaluateDiff(tt.files, tt.issueBody, policy))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for reason, path := range tt.want {
				gp, ok := got[reason]
				if !ok {
					t.Errorf("missing violation %q (got %v)", reason, got)
					continue
				}
				if gp != path {
					t.Errorf("violation %q: got path %q, want %q", reason, gp, path)
				}
			}
		})
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{".github/**", ".github", true},
		{".github/**", ".github/workflows/ci.yml", true},
		{".github/**", ".githubfoo/x", false},
		{"Dockerfile", "Dockerfile", true},
		{"Dockerfile", "sub/Dockerfile", false},
		{"**/Dockerfile", "sub/Dockerfile", true},
		{"**/Dockerfile", "Dockerfile", true},
		{"**/Dockerfile", "Dockerfile.dev", false},
		{"package*.json", "package-lock.json", true},
		{"package*.json", "package.json", true},
		{"package*.json", "sub/package.json", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.path); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func manyFiles(n int) []*gh.CommitFile {
	var fs []*gh.CommitFile
	for i := 0; i < n; i++ {
		fs = append(fs, file("docs/f"+string(rune('a'+i%26))+string(rune('0'+i/26))+".md", 1, 0))
	}
	return fs
}
