package models

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/platform/ai"
	"github.com/remarqable/tmpio/internal/platform/db"
)

// fakeAI returns scripted replies and records the prompt it saw.
type fakeAI struct {
	reply string
	err   error
	seen  string
}

// Enabled implements ai.Completer: a stub is always ready.
func (f *fakeAI) Enabled(context.Context) bool { return true }

func (f *fakeAI) Complete(_ context.Context, _ string, user string, _ int) (ai.Result, error) {
	f.seen = user
	if f.err != nil {
		return ai.Result{}, f.err
	}
	return ai.Result{Text: f.reply, InputTokens: 100, OutputTokens: 20}, nil
}
func (f *fakeAI) Model() string { return "fake-model" }

func TestSuggestPlacement(t *testing.T) {
	db.ConnectTest(t)
	ctx := context.Background()
	o := testOps()
	p, _, _ := newOwner(t, "place@x.test")
	_, err := o.Write(ctx, p, WriteInput{Path: "/research/circle.md", Content: "---\ntitle: Circle\ntags: [community]\n---\n# Circle\n", RequestID: rid()})
	require.NoError(t, err)
	_, err = o.Write(ctx, p, WriteInput{Path: "/mobile-app/index.md", Content: "# Mobile app\n", RequestID: rid()})
	require.NoError(t, err)

	t.Run("heuristic: folder named in the text wins, title becomes the name", func(t *testing.T) {
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "# Sprint review\n\nNotes from the mobile app standup.\n"})
		require.NoError(t, err)
		assert.Equal(t, "heuristic", pl.Source)
		assert.Equal(t, "/mobile-app/sprint-review.md", pl.Path)
		assert.Equal(t, KindPage, pl.Kind)
		assert.False(t, pl.NewDirectory)
		assert.EqualValues(t, 0, pl.ExpectedRevision)
	})
	t.Run("heuristic: nothing matches goes to the inbox; csv hint keeps the extension", func(t *testing.T) {
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "a,b\n1,2\n", Filename: "Q3 Metrics.csv"})
		require.NoError(t, err)
		assert.Equal(t, "/inbox/q3-metrics.csv", pl.Path)
		assert.Equal(t, KindFile, pl.Kind)
		assert.True(t, pl.NewDirectory)
	})
	t.Run("heuristic: JSON content without a hint becomes a .json file", func(t *testing.T) {
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: `{"a": 1}`})
		require.NoError(t, err)
		assert.Equal(t, ".json", pl.Path[len(pl.Path)-5:])
	})

	f := &fakeAI{reply: "```json\n{\"directory\": \"/research\", \"name\": \"Circle Pricing!\", \"title\": \"Circle pricing\", \"reason\": \"It is about Circle.\", \"alternatives\": [\"/mobile-app\", \"/Admin\"]}\n```"}
	o.AI = f
	o.AIMaxCallsPerHour = 100
	t.Run("ai: nothing is sent until the owner opts in", func(t *testing.T) {
		o2 := testOps()
		gate := &fakeAI{reply: `{"directory":"/research","name":"x"}`}
		o2.AI = gate
		pl, err := o2.SuggestPlacement(ctx, p, PlacementInput{Content: "# Anything\n\nplain note\n"})
		require.NoError(t, err)
		assert.Equal(t, "heuristic", pl.Source)
		assert.Empty(t, gate.seen, "the model must not be called while ai_filing_enabled is false")
	})
	require.NoError(t, SetTenantAIFiling(ctx, p.TenantID, true))
	t.Run("ai: reply is cleaned to the grammar and validated against the tree", func(t *testing.T) {
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "Circle charges $89 per month.", Hint: "pricing note"})
		require.NoError(t, err)
		assert.Equal(t, "ai", pl.Source)
		assert.Equal(t, "/research/circle-pricing.md", pl.Path)
		assert.Equal(t, "Circle pricing", pl.Title)
		assert.Equal(t, []string{"/mobile-app"}, pl.Alternatives, "reserved roots are dropped from alternatives")
		assert.Contains(t, f.seen, "P /research/circle.md  \"Circle\"  [community]", "snapshot lists pages with titles and tags")
		assert.Contains(t, f.seen, "D /mobile-app/  \"Mobile app\"", "index page title names its folder")
		assert.Contains(t, f.seen, "SENDER'S HINT: pricing note")
	})
	t.Run("ai: an occupied path is reported with its revision and a free variant", func(t *testing.T) {
		f.reply = `{"directory": "/research", "name": "circle"}`
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "More about Circle."})
		require.NoError(t, err)
		assert.True(t, pl.Exists)
		assert.EqualValues(t, 1, pl.ExistingRevision)
		assert.EqualValues(t, 1, pl.ExpectedRevision)
		assert.Equal(t, "/research/circle-2.md", pl.UniquePlacementPath())
	})
	t.Run("ai: a good folder with no name keeps the folder and names from the title", func(t *testing.T) {
		f.reply = `{"directory": "/research", "name": ""}`
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "# Slack pricing tiers\n\nPro is $8.75."})
		require.NoError(t, err)
		assert.Equal(t, "ai", pl.Source)
		assert.Equal(t, "/research/slack-pricing-tiers.md", pl.Path)
	})
	t.Run("ai: reserved or invalid folders fall back to the heuristic", func(t *testing.T) {
		f.reply = `{"directory": "/admin", "name": "x"}`
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "Unrelated text."})
		require.NoError(t, err)
		assert.Equal(t, "heuristic", pl.Source)
		assert.Equal(t, "/inbox", pl.Directory)
	})
	t.Run("ai: transport errors and garbage fall back and are audited", func(t *testing.T) {
		f.err = errors.New("boom")
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "# Anything\n"})
		require.NoError(t, err)
		assert.Equal(t, "heuristic", pl.Source)
		f.err = nil
		f.reply = "I cannot help with that."
		pl, err = o.SuggestPlacement(ctx, p, PlacementInput{Content: "# Anything\n"})
		require.NoError(t, err)
		assert.Equal(t, "heuristic", pl.Source)
		sum, err := o.AISummary(ctx, p.TenantID)
		require.NoError(t, err)
		assert.EqualValues(t, 3, sum.Calls24h, "three successful model answers")
		assert.EqualValues(t, 3, sum.Fallbacks24h)
		assert.EqualValues(t, 600, sum.Tokens24h, "five replies carried usage")
	})
	t.Run("ai: hourly budget switches to the heuristic without calling the model", func(t *testing.T) {
		o.AIMaxCallsPerHour = 1
		f.seen = ""
		f.reply = `{"directory": "/research", "name": "budget"}`
		pl, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "budget test"})
		require.NoError(t, err)
		assert.Equal(t, "heuristic", pl.Source)
		assert.Empty(t, f.seen)
	})
	t.Run("guards: empty content, share principals and oversize input are refused", func(t *testing.T) {
		_, err := o.SuggestPlacement(ctx, p, PlacementInput{Content: "  "})
		assert.Error(t, err)
		_, err = o.SuggestPlacement(ctx, Principal{Kind: PrincipalShareLink, TenantID: p.TenantID, Scopes: AllScopes}, PlacementInput{Content: "x"})
		assert.Error(t, err)
	})
}

func TestPlacementHelpers(t *testing.T) {
	assert.Equal(t, "/research/sub", cleanDir(" Research/Sub/ "))
	assert.Equal(t, "", cleanDir(""), "root is not a placement")
	assert.Equal(t, "", cleanDir("/api"))
	assert.Equal(t, "", cleanDir("/a/b/c/d/e"))
	assert.Equal(t, "q3-metrics_v2", cleanName("  Q3 Metrics_v2.md "))
	assert.Equal(t, "", cleanName("!!!"))
	assert.Equal(t, "Title here", headingOf("---\ntitle: Title here\n---\n# Other\n"))
	assert.Equal(t, "Other", headingOf("\n\n## Other\ntext"))
	assert.Equal(t, "plain first line", headingOf("plain first line\nmore"))
	assert.Equal(t, `{"a":1}`, extractJSON("Sure! {\"a\":1} done"))
	assert.Equal(t, ".csv", placementExt("data.CSV", "x"))
	assert.Equal(t, ".md", placementExt("notes.docx", "x"))
	assert.Equal(t, ".json", placementExt("", `[1,2]`))
}

// jsonFakeAI implements the optional schema-validated path, so the reply
// arrives as a bare object the way the real client now returns one.
type jsonFakeAI struct {
	fakeAI
	sawSchema any
	sawTool   string
}

func (f *jsonFakeAI) CompleteJSON(_ context.Context, _ string, user string, _ int, name string, schema any) (ai.Result, error) {
	f.seen = user
	f.sawTool, f.sawSchema = name, schema
	return ai.Result{Text: f.reply}, nil
}

// TestPlacementPrefersSchemaValidatedReply: when the client can force a shape,
// the filer uses it, and the reply needs no scraping out of prose.
func TestPlacementPrefersSchemaValidatedReply(t *testing.T) {
	f := &jsonFakeAI{fakeAI: fakeAI{reply: `{"directory":"/business/acme","name":"q3-investor-update","title":"Q3 investor update"}`}}
	o := &Ops{AI: f}

	res, err := o.completePlacement(t.Context(), "some document")
	require.NoError(t, err)
	assert.Equal(t, "file_document", f.sawTool, "the answer is recorded through a tool")
	assert.NotNil(t, f.sawSchema, "the reply shape is handed to the API, not only described in prose")
	assert.Equal(t, `{"directory":"/business/acme","name":"q3-investor-update","title":"Q3 investor update"}`, res.Text)
}

// TestPlacementFallsBackToText keeps a client without the optional method
// working: the answer is scraped out of whatever came back.
func TestPlacementFallsBackToText(t *testing.T) {
	f := &fakeAI{reply: "```json\n{\"directory\":\"/research\"}\n```"}
	o := &Ops{AI: f}
	res, err := o.completePlacement(t.Context(), "some document")
	require.NoError(t, err)
	assert.Contains(t, res.Text, "/research")
}

// TestPromptClosesTheTopLevel is the rule that stops the tree sprawling: the
// categories are a fixed list, not a list of examples. The nine suggested
// "for example" categories are what produced /notebook and /writing beside
// /personal on a real site.
func TestPromptClosesTheTopLevel(t *testing.T) {
	assert.NotContains(t, placeSystemPrompt, "for example business, personal",
		"the categories must not read as suggestions")
	assert.Contains(t, placeSystemPrompt, "Never invent a sixth top-level folder")
	for _, want := range []string{"/business", "/personal", "/projects", "/research", "/inbox"} {
		assert.Contains(t, placeSystemPrompt, want)
	}
	// Folders that used to be offered as categories and are now second level.
	for _, gone := range []string{"writing, finance, health, learning", "/notebook"} {
		assert.NotContains(t, placeSystemPrompt, gone)
	}
}

// TestPromptRulesTheReviewAsked covers the other three: the tree is evidence
// of the owner's own vocabulary, a mixed document is filed by primary purpose,
// and a recurring document may lead with a date.
func TestPromptRulesTheReviewAsked(t *testing.T) {
	assert.Contains(t, placeSystemPrompt, "evidence of how the owner organizes")
	assert.Contains(t, placeSystemPrompt, "/clients", "an existing vocabulary is named as an example")
	assert.Contains(t, placeSystemPrompt, "primary purpose")
	assert.Contains(t, placeSystemPrompt, "ISO date")
	// inbox is a destination now, so it cannot also be a forbidden root.
	reserved := placeSystemPrompt[strings.Index(placeSystemPrompt, "Never use these root folders:"):]
	assert.NotContains(t, reserved, "inbox")
}
