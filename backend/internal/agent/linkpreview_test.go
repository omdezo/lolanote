package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"qomranote/backend/internal/service"
)

// fakeLinks answers Resolve with one fixed page, or refuses.
type fakeLinks struct {
	meta  *service.LinkMetadata
	fails bool
	calls int
}

func (f *fakeLinks) Resolve(context.Context, string) (*service.LinkMetadata, error) {
	f.calls++
	if f.fails {
		return nil, errors.New("could not reach it")
	}
	return f.meta, nil
}

func linkStaging(l *fakeLinks) *staging {
	s := reachStaging()
	s.links = l
	return s
}

// The compiler used to write showPreview:false and showDescription:false on
// every link it made — not an omission but a decision, and one that made an
// agent-placed link WORSE than a default one: a grey card that had explicitly
// opted out of being rich, on a product whose own drop handler resolves the
// page. The resolver was already wired into the planner and used by read_url.
func TestLinkPreview_AnAgentMadeLinkIsAsRichAsADroppedOne(t *testing.T) {
	links := &fakeLinks{meta: &service.LinkMetadata{
		Title:        "Deakins on lighting night exteriors",
		Description:  "A conversation about sodium vapour and moonlight.",
		ThumbnailURL: "https://img.example.test/thumb.jpg",
		SiteName:     "YouTube",
		EmbedType:    "youtube",
	}}
	s := linkStaging(links)

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "b1", URL: "https://youtube.test/watch?v=x"},
		call(s, toolCreateLink))
	if out.IsError {
		t.Fatalf("staging a link failed: %s", out.Content)
	}
	if links.calls != 1 {
		t.Fatalf("the page was fetched %d time(s), want 1", links.calls)
	}

	a := s.plan.Actions[len(s.plan.Actions)-1]
	// A title the model did not supply comes from the page, so the card is not
	// a bare URL pretending to be a name.
	if a.Title != "Deakins on lighting night exteriors" {
		t.Errorf("title = %q, want the page's own", a.Title)
	}

	// Assert on the COMPILED content, which is what LinkCard actually reads.
	content, _ := createOp(a).Changes["content"].(map[string]any)
	if content["showPreview"] != true {
		t.Errorf("the card still opts out of its own preview: %v", content["showPreview"])
	}
	if content["thumbnailUrl"] != "https://img.example.test/thumb.jpg" {
		t.Errorf("thumbnailUrl = %v", content["thumbnailUrl"])
	}
	if content["embedType"] != "youtube" {
		t.Errorf("embedType = %v — the card cannot become a playable embed", content["embedType"])
	}
	if content["siteName"] != "YouTube" {
		t.Errorf("siteName = %v", content["siteName"])
	}
	if content["showDescription"] != true || content["description"] == "" {
		t.Errorf("the description was fetched and then hidden: %v", content)
	}
}

// A page that will not load is not an error. The whole point of the fetch is
// that the card is richer when it works, and exactly what it always was when it
// does not — a failure here must never cost the person their link.
func TestLinkPreview_AnUnreachablePageStillProducesTheLink(t *testing.T) {
	s := linkStaging(&fakeLinks{fails: true})

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "b1", URL: "https://offline.test/page", Title: "Reference"},
		call(s, toolCreateLink))
	if out.IsError {
		t.Fatalf("a failed fetch cost the person their link: %s", out.Content)
	}
	a := s.plan.Actions[len(s.plan.Actions)-1]
	if a.Preview != nil {
		t.Errorf("a failed fetch invented a preview: %+v", a.Preview)
	}
	content, _ := createOp(a).Changes["content"].(map[string]any)
	if content["url"] != "https://offline.test/page" || content["title"] != "Reference" {
		t.Errorf("the bare link is wrong: %v", content)
	}
	if content["showPreview"] != false {
		t.Errorf("preview is on with nothing to show: %v", content)
	}
}

// A thumbnail url comes off a page the MODEL chose, and it lands in an <img
// src> on the person's board. Anything that is not http(s) is dropped rather
// than rendered.
func TestLinkPreview_AThumbnailThatIsNotAURLIsDropped(t *testing.T) {
	s := linkStaging(&fakeLinks{meta: &service.LinkMetadata{
		Title:        "Ordinary page",
		ThumbnailURL: "javascript:alert(document.cookie)",
	}})

	out := s.runCreateBoard(context.Background(),
		&toolArgs{ParentID: "b1", URL: "https://example.test/a"},
		call(s, toolCreateLink))
	if out.IsError {
		t.Fatalf("staging failed: %s", out.Content)
	}
	a := s.plan.Actions[len(s.plan.Actions)-1]
	if a.Preview == nil {
		t.Fatal("the preview was dropped entirely")
	}
	if a.Preview.ThumbnailURL != "" {
		t.Errorf("a javascript: url reached an <img src>: %q", a.Preview.ThumbnailURL)
	}
}

// read_url fetched the site name and the embed type and threw both away, so the
// model could not tell a YouTube video from a blog post — and therefore had no
// basis for deciding a card should be a playable embed.
func TestLinkPreview_ReadURLReportsWhatItLearned(t *testing.T) {
	s := linkStaging(&fakeLinks{meta: &service.LinkMetadata{
		Title: "Deakins on lighting", SiteName: "YouTube", EmbedType: "youtube",
	}})

	out := s.runReadURL(context.Background(),
		&toolArgs{URL: "https://youtube.test/watch?v=x"}, call(s, toolReadURL))
	if out.IsError {
		t.Fatalf("read_url failed: %s", out.Content)
	}
	for _, want := range []string{"⟨web⟩", "YouTube", "youtube"} {
		if !strings.Contains(out.Content, want) {
			t.Errorf("read_url dropped %q from what it learned: %s", want, out.Content)
		}
	}
}
