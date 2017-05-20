// Copyright 2016 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// Author: Tamir Duberstein (tamird@gmail.com)

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-github/github"
)

func TestRunGH(t *testing.T) {
	const (
		expOwner    = "cockroachdb"
		expRepo     = "cockroach"
		envPkg      = "foo/bar/baz"
		envTags     = "deadlock"
		envGoFlags  = "race"
		sha         = "abcd123"
		serverURL   = "https://teamcity.example.com"
		buildID     = 8008135
		issueID     = 1337
		issueNumber = 30
	)

	for key, value := range map[string]string{
		teamcityVCSNumberEnv: sha,
		teamcityServerURLEnv: serverURL,
		teamcityBuildIDEnv:   strconv.Itoa(buildID),

		pkgEnv:     cockroachPkgPrefix + envPkg,
		tagsEnv:    envTags,
		goFlagsEnv: envGoFlags,
	} {
		if val, ok := os.LookupEnv(key); ok {
			defer func() {
				if err := os.Setenv(key, val); err != nil {
					t.Error(err)
				}
			}()
		} else {
			defer func() {
				if err := os.Unsetenv(key); err != nil {
					t.Error(err)
				}
			}()
		}

		if err := os.Setenv(key, value); err != nil {
			t.Fatal(err)
		}
	}

	parameters := "```\n" + strings.Join([]string{
		tagsEnv + "=" + envTags,
		goFlagsEnv + "=" + envGoFlags,
	}, "\n") + "\n```"

	for fileName, expectations := range map[string]struct {
		packageName string
		testName    string
		body        string
	}{
		"stress-failure": {
			packageName: envPkg,
			testName:    "TestReplicateQueueRebalance",
			body: "	<autogenerated>:12: storage/replicate_queue_test.go:103, condition failed to evaluate within 45s: not balanced: [10 1 10 1 8]",
		},
		"stress-fatal": {
			packageName: envPkg,
			testName:    "TestGossipHandlesReplacedNode",
			body:        "F170517 07:33:43.763059 69575 storage/replica.go:1360  [n3,s3,r1/3:/M{in-ax}] on-disk and in-memory state diverged:",
		},
	} {
		for _, foundIssue := range []bool{true, false} {
			testName := fileName
			if foundIssue {
				testName = testName + "-existing-issue"
			}
			t.Run(testName, func(t *testing.T) {
				file, err := os.Open(filepath.Join("testdata", fileName))
				if err != nil {
					t.Fatal(err)
				}

				reString := fmt.Sprintf(`(?s)\ASHA: https://github.com/cockroachdb/cockroach/commits/%s

Parameters:
%s

Stress build found a failed test: %s`,
					regexp.QuoteMeta(sha),
					regexp.QuoteMeta(parameters),
					regexp.QuoteMeta(fmt.Sprintf("%s/viewLog.html?buildId=%d&tab=buildLog", serverURL, buildID)),
				)

				issueBodyRe, err := regexp.Compile(
					fmt.Sprintf(reString+`

.*
%s
`, regexp.QuoteMeta(expectations.body)),
				)
				if err != nil {
					t.Fatal(err)
				}
				commentBodyRe, err := regexp.Compile(reString)
				if err != nil {
					t.Fatal(err)
				}

				issueCount := 0
				commentCount := 0
				postIssue := func(_ context.Context, owner string, repo string, issue *github.IssueRequest) (*github.Issue, *github.Response, error) {
					issueCount++
					if owner != expOwner {
						t.Fatalf("got %s, expected %s", owner, expOwner)
					}
					if repo != expRepo {
						t.Fatalf("got %s, expected %s", repo, expRepo)
					}
					if expected := fmt.Sprintf("%s: %s failed under stress", expectations.packageName, expectations.testName); *issue.Title != expected {
						t.Fatalf("got %s, expected %s", *issue.Title, expected)
					}
					if !issueBodyRe.MatchString(*issue.Body) {
						t.Fatalf("got:\n%s\nexpected:\n%s", *issue.Body, issueBodyRe)
					}
					if length := len(*issue.Body); length > githubIssueBodyMaximumLength {
						t.Fatalf("issue length %d exceeds (undocumented) maximum %d", length, githubIssueBodyMaximumLength)
					}
					return &github.Issue{ID: github.Int(issueID)}, nil, nil
				}
				searchIssues := func(_ context.Context, query string, opt *github.SearchOptions) (*github.IssuesSearchResult, *github.Response, error) {
					total := 0
					if foundIssue {
						total = 1
					}
					return &github.IssuesSearchResult{
						Total: &total,
						Issues: []github.Issue{
							{Number: github.Int(issueNumber)},
						},
					}, nil, nil
				}
				postComment := func(_ context.Context, owner string, repo string, number int, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
					if owner != expOwner {
						t.Fatalf("got %s, expected %s", owner, expOwner)
					}
					if repo != expRepo {
						t.Fatalf("got %s, expected %s", repo, expRepo)
					}
					if !commentBodyRe.MatchString(*comment.Body) {
						t.Fatalf("got:\n%s\nexpected:\n%s", *comment.Body, issueBodyRe)
					}
					if length := len(*comment.Body); length > githubIssueBodyMaximumLength {
						t.Fatalf("comment length %d exceeds (undocumented) maximum %d", length, githubIssueBodyMaximumLength)
					}
					commentCount++

					return nil, nil, nil
				}

				if err := runGH(context.Background(), file, postIssue, searchIssues, postComment); err != nil {
					t.Fatal(err)
				}
				expectedIssues := 1
				expectedComments := 0
				if foundIssue {
					expectedIssues = 0
					expectedComments = 1
				}
				if issueCount != expectedIssues {
					t.Fatalf("%d issues were posted, expected %d", issueCount, expectedIssues)
				}
				if commentCount != expectedComments {
					t.Fatalf("%d comments were posted, expected %d", commentCount, expectedComments)
				}
			})
		}
	}
}
