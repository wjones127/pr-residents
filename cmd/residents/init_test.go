package main

import (
	"strings"
	"testing"
)

func TestParseRepos(t *testing.T) {
	got := parseRepos("  lance-format/lance, lancedb/lancedb  lance-format/lance ")
	want := []string{"lance-format/lance", "lancedb/lancedb"} // trimmed, deduped
	if len(got) != len(want) {
		t.Fatalf("parseRepos = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseRepos[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if len(parseRepos("   ")) != 0 {
		t.Error("blank input should yield no repos")
	}
}

func TestOrgsOf(t *testing.T) {
	got := orgsOf([]string{"lancedb/lancedb", "lance-format/lance", "lancedb/lance-python"})
	want := []string{"lance-format", "lancedb"} // distinct, sorted
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("orgsOf = %v, want %v", got, want)
	}
}

func TestRenderConfigYAML(t *testing.T) {
	out := renderConfigYAML(initAnswers{
		GithubLogin: "wjones127",
		Repos:       []string{"lance-format/lance", "lancedb/lancedb"},
		Interests:   []string{"rust/lance-index"},
	})
	for _, want := range []string{
		"github_login: wjones127",
		"  - lance-format/lance",
		"  - lancedb/lancedb",
		"  - rust/lance-index",
		"engine: claude",
		"port: 8787",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderConfigYAMLEmptyInterests(t *testing.T) {
	out := renderConfigYAML(initAnswers{GithubLogin: "x", Repos: []string{"a/b"}})
	if !strings.Contains(out, "interests: []") {
		t.Errorf("empty interests should render []: \n%s", out)
	}
}

func TestStateDirOf(t *testing.T) {
	if got := stateDirOf("/cfg", ""); got != "/cfg/state" {
		t.Errorf("default state dir = %q", got)
	}
	if got := stateDirOf("/cfg", "/custom"); got != "/custom" {
		t.Errorf("explicit state dir = %q", got)
	}
}
