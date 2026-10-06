package delegation_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/meetproxy/internal/delegation"
)

func TestDelegationValidate(t *testing.T) {
	t.Parallel()
	type args struct {
		id      string
		when    string
		channel string
		host    string
		link    string
		do      string
		post    string
		approve string
	}
	const (
		channel = delegation.WhenChannel
		mention = delegation.WhenMention
		auto    = delegation.PostAuto
	)
	tcs := []struct {
		name   string
		args   args
		failed bool
	}{
		{"accepts a channel delegation", args{"alerts", channel, "C090SB4V8L8", "acme.slack.com", "", delegation.DoInvestigate, auto, ""}, false},
		{"accepts a skill of another plugin", args{"alerts", channel, "C090SB4V8L8", "acme.slack.com", "", "ops:incident-triage", auto, ""}, false},
		{"accepts a mention with a pull request link", args{"pr", mention, "", "", delegation.LinkPR, delegation.DoReview, auto, delegation.ApproveAny}, false},
		{"accepts a review request", args{"rr", delegation.WhenReviewRequest, "", "", "", delegation.DoReview, delegation.PostAsk, delegation.ApproveNever}, false},
		{"refuses the default id", args{"default", channel, "C090SB4V8L8", "acme.slack.com", "", delegation.DoInvestigate, auto, ""}, true},
		{"refuses an id of the built in delegations", args{"default-review", mention, "", "", "", delegation.DoReview, auto, ""}, true},
		{"refuses an uppercase id", args{"Alerts", mention, "", "", "", delegation.DoAnswer, auto, ""}, true},
		{"refuses an unknown when", args{"alerts", "always", "", "", "", delegation.DoAnswer, auto, ""}, true},
		{"refuses a channel delegation without a host", args{"alerts", channel, "C090SB4V8L8", "", "", delegation.DoInvestigate, auto, ""}, true},
		{"refuses a channel name", args{"alerts", channel, "#ops", "acme.slack.com", "", delegation.DoInvestigate, auto, ""}, true},
		{"refuses an unknown link", args{"pr", mention, "", "", "gitlab-mr", delegation.DoReview, auto, ""}, true},
		{"refuses an unknown post", args{"alerts", channel, "C090SB4V8L8", "acme.slack.com", "", delegation.DoInvestigate, "later", ""}, true},
		{"refuses an unknown approve", args{"alerts", channel, "C090SB4V8L8", "acme.slack.com", "", delegation.DoInvestigate, auto, "maybe"}, true},
		{"refuses a skill with spaces", args{"alerts", channel, "C090SB4V8L8", "acme.slack.com", "", "rm -rf", auto, ""}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := delegation.Delegation{
				Id: tc.args.id, When: tc.args.when, Channel: tc.args.channel, Host: tc.args.host, Link: tc.args.link,
				Do: tc.args.do, Post: tc.args.post, Approve: tc.args.approve,
			}
			assert.Equal(t, tc.failed, d.Validate() != nil)
		})
	}
}

func TestDelegationMayApprove(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name     string
		do       string
		approve  string
		fromUser bool
		want     bool
	}{
		{"a review by default approves only the user's request", delegation.DoReview, "", true, true},
		{"a review by default comments for others", delegation.DoReview, "", false, false},
		{"any approves for others", delegation.DoReview, delegation.ApproveAny, false, true},
		{"never comments for the user too", delegation.DoReview, delegation.ApproveNever, true, false},
		{"only reviews approve", delegation.DoAnswer, delegation.ApproveAny, true, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := delegation.Delegation{Do: tc.do, Approve: tc.approve}
			assert.Equal(t, tc.want, d.MayApprove(tc.fromUser))
		})
	}
}

func TestStore(t *testing.T) {
	t.Parallel()
	s := delegation.New(t.TempDir())
	pr := delegation.Delegation{Id: "PR", When: delegation.WhenMention, Link: delegation.LinkPR, Do: delegation.DoReview, Post: delegation.PostAuto}
	require.NoError(t, s.Put(pr))
	pr.Post = delegation.PostAsk
	require.NoError(t, s.Put(pr))

	ds, err := s.List()
	require.NoError(t, err)
	require.Len(t, ds, 4)
	assert.Equal(t, []string{"pr", "default-review", "default-review-request", "default"}, []string{ds[0].Id, ds[1].Id, ds[2].Id, ds[3].Id})
	assert.Equal(t, delegation.PostAsk, ds[0].Post, "the same id replaces the delegation")

	require.NoError(t, s.Remove(" PR "), "the id is matched as it was stored")
	assert.Error(t, s.Remove("pr"))
	ds, err = s.List()
	require.NoError(t, err)
	assert.Equal(t, []delegation.Delegation{delegation.DefaultReview, delegation.DefaultReviewRequest, delegation.Default}, ds)
}

// Sessions adding delegations at once never lose one
func TestStorePut_Race(t *testing.T) {
	t.Parallel()
	s := delegation.New(t.TempDir())
	const writers = 16
	var wg sync.WaitGroup
	for n := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := delegation.Delegation{Id: fmt.Sprintf("d%d", n), When: delegation.WhenMention, Do: delegation.DoAnswer, Post: delegation.PostAuto}
			assert.NoError(t, s.Put(d))
		}()
	}
	wg.Wait()

	ds, err := s.List()

	require.NoError(t, err)
	assert.Len(t, ds, writers+3)
}

func TestPick(t *testing.T) {
	t.Parallel()
	ds := []delegation.Delegation{
		{Id: "alerts", When: delegation.WhenChannel, From: []string{"datadog"}, Words: []string{"triggered"}, Do: delegation.DoInvestigate},
		delegation.DefaultReview,
		delegation.DefaultReviewRequest,
		delegation.Default,
	}
	type want struct {
		id string
		ok bool
	}
	tcs := []struct {
		name string
		key  string
		msg  delegation.Message
		want want
	}{
		{
			"a pull request link picks the built in review", delegation.WhenMention,
			delegation.Message{Text: "see https://github.com/o/r/pull/3"}, want{"default-review", true},
		},
		{"a plain mention falls back to the default", delegation.WhenMention, delegation.Message{Text: "where is alloc"}, want{"default", true}},
		{
			"a question on a pull request is no review", delegation.WhenMention,
			delegation.Message{Link: "https://github.com/o/r/pull/3#issuecomment-1", Text: "where is retry set"}, want{"default", true},
		},
		{
			"a review request picks the built in review request", delegation.WhenReviewRequest,
			delegation.Message{Link: "https://github.com/o/r/pull/3"}, want{"default-review-request", true},
		},
		{"a mention never picks a channel delegation", delegation.WhenMention, delegation.Message{Text: "Triggered", Author: "Datadog"}, want{"default", true}},
		{"an alert matches author and word without case", "alerts", delegation.Message{Text: "Triggered: cpu", Author: "Datadog"}, want{"alerts", true}},
		{"an author id matches too", "alerts", delegation.Message{Text: "TRIGGERED", From: "DATADOG"}, want{"alerts", true}},
		{"a recovery does not match", "alerts", delegation.Message{Text: "Recovered: cpu", Author: "Datadog"}, want{"", false}},
		{"another author does not match", "alerts", delegation.Message{Text: "Triggered", Author: "Jed"}, want{"", false}},
		{
			"a display name that only contains the author does not match", "alerts",
			delegation.Message{Text: "Triggered", Author: "not datadog"}, want{"", false},
		},
		{"an id that only contains the author does not match", "alerts", delegation.Message{Text: "Triggered", From: "datadog2"}, want{"", false}},
		{"an id of no channel delegation matches nothing", "default", delegation.Message{Text: "where is alloc"}, want{"", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, ok := delegation.Pick(ds, tc.key, tc.msg)
			assert.Equal(t, tc.want, want{d.Id, ok})
		})
	}
}

func TestDelegationMatches(t *testing.T) {
	t.Parallel()
	d := delegation.Delegation{Link: delegation.LinkPR, Words: []string{"urgent"}}
	pr := "https://github.com/o/svc/pull/9"
	tcs := []struct {
		name string
		msg  delegation.Message
		want bool
	}{
		{"a pull request link with the word matches", delegation.Message{Text: "URGENT see " + pr}, true},
		{"a pull request link without the word does not match", delegation.Message{Text: "see " + pr}, false},
		{"the word without a pull request link does not match", delegation.Message{Text: "urgent please"}, false},
		{"a link only in the message link does not count", delegation.Message{Link: pr, Text: "urgent please"}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, d.Matches(tc.msg))
		})
	}
}

func TestDelegationTarget(t *testing.T) {
	t.Parallel()
	pr := "https://github.com/o/svc/pull/9"
	tcs := []struct {
		name       string
		delegation delegation.Delegation
		msg        delegation.Message
		want       []string
	}{
		{"a mention review takes the pull request of the text", delegation.DefaultReview, delegation.Message{Text: "see " + pr}, []string{pr, "svc"}},
		{
			"a review request takes the pull request it links", delegation.DefaultReviewRequest,
			delegation.Message{Link: pr, Text: "see https://github.com/o/web/pull/1"}, []string{pr, "svc"},
		},
		{"an answer has no target", delegation.Default, delegation.Message{Text: "see " + pr}, []string{"", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			link, repo := tc.delegation.Target(tc.msg)
			assert.Equal(t, tc.want, []string{link, repo})
		})
	}
}

func TestPullRequest(t *testing.T) {
	t.Parallel()
	link, repo := delegation.PullRequest("<https://github.com/Buzzvil/buzzscreen-api/pull/7880|#7880> please")
	assert.Equal(t, []string{"https://github.com/Buzzvil/buzzscreen-api/pull/7880", "buzzscreen-api"}, []string{link, repo})
	link, repo = delegation.PullRequest("no link")
	assert.Equal(t, []string{"", ""}, []string{link, repo})
}
