package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
)

// fakeAttachments is the smallest thing that answers Get.
type fakeAttachments struct{ items map[string]*domain.Attachment }

func (f *fakeAttachments) Get(_ context.Context, id string) (*domain.Attachment, error) {
	if a, ok := f.items[id]; ok {
		return a, nil
	}
	return nil, domain.ErrNotFound
}
func (f *fakeAttachments) Insert(context.Context, *domain.Attachment) error { return nil }
func (f *fakeAttachments) Update(context.Context, *domain.Attachment) error { return nil }
func (f *fakeAttachments) Delete(context.Context, string) error             { return nil }
func (f *fakeAttachments) DeleteByOwner(context.Context, string) error      { return nil }
func (f *fakeAttachments) StalePresigned(context.Context, time.Time) ([]*domain.Attachment, error) {
	return nil, nil
}

func attachStaging(attached ...string) *staging {
	return &staging{
		runID: "r1",
		scope: &BoardScope{
			Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
			Elements: map[string]*domain.Element{},
		},
		task: TaskSpec{
			Budget:        Budget{MaxActions: 60},
			AttachmentIDs: attached,
		},
		plan:        &Plan{},
		created:     map[string]ActionKind{},
		failedCalls: map[string]int{},
		quotas:      newQuotas(),
		emit:        func(EventType, string, map[string]any) {},
		files: &fakeAttachments{items: map[string]*domain.Attachment{
			"att-1": {
				ID: "att-1", Filename: "image.png", ContentType: "image/png",
				PublicURL: "https://example.test/att-1.png",
			},
			"att-pending": {ID: "att-pending", Filename: "slow.png", ContentType: "image/png"},
			"att-other":   {ID: "att-other", Filename: "someone-elses.png", PublicURL: "https://x/y.png"},
			"att-pdf": {
				ID: "att-pdf", Filename: "shooting-schedule.pdf", ContentType: "application/pdf",
				Size: 91_234, PublicURL: "https://example.test/att-pdf.pdf",
			},
		}},
	}
}

// Asked "put it in the first scene this image", with the image attached and
// already read, the agent answered: "I am unable to directly add an image
// without its content or a URL." True, and reading as broken — the file was
// uploaded, in scope, and it had looked at it. There was simply no action in
// the whole plan vocabulary that produced an IMAGE.
func TestAttachment_CanBePlacedOnTheBoard(t *testing.T) {
	s := attachStaging("att-1")

	out := s.runPlaceFile(context.Background(),
		&toolArgs{AttachmentID: "att-1", ParentID: "b1", Title: "Hands working clay"},
		&reply{staging: s})
	if out.IsError {
		t.Fatalf("placing an attached image failed: %s", out.Content)
	}
	if len(s.plan.Actions) != 1 {
		t.Fatalf("staged %d actions, want 1", len(s.plan.Actions))
	}

	a := s.plan.Actions[0]
	if a.Kind != ActPlaceFile {
		t.Errorf("kind = %s", a.Kind)
	}
	if a.Type() != domain.TypeImage {
		t.Errorf("produces a %s, want an IMAGE", a.Type())
	}
	if a.URL == "" || a.AssigneeID != "att-1" {
		t.Errorf("action does not carry the attachment: %+v", a)
	}

	// And it compiles to what the renderer reads: {url, attachmentId, caption}.
	op := createOp(a)
	content, _ := op.Changes["content"].(map[string]any)
	for _, key := range []string{"url", "attachmentId", "caption"} {
		if content[key] == nil || content[key] == "" {
			t.Errorf("compiled content is missing %q: %v", key, content)
		}
	}
	if content["caption"] != "Hands working clay" {
		t.Errorf("caption = %v", content["caption"])
	}
}

// place_file's own description promises "an image card if it is one, a file card
// otherwise". The type came from the ActionKind — a pure function that cannot
// see a mime type — so every attachment compiled as an IMAGE and an attached PDF
// became <img src={pdf}>: a broken picture, no filename, no download, and
// unreadable to the next run, which was told it was looking at a picture.
func TestAttachment_APDFBecomesAFileCardNotABrokenImage(t *testing.T) {
	s := attachStaging("att-pdf")

	out := s.runPlaceFile(context.Background(),
		&toolArgs{AttachmentID: "att-pdf", ParentID: "b1"}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("placing an attached PDF failed: %s", out.Content)
	}
	a := s.plan.Actions[0]
	if a.Type() != domain.TypeFile {
		t.Fatalf("a PDF produces a %s, want a FILE", a.Type())
	}

	// The compiled shape must be byte-identical to what a human drop writes
	// (BoardCanvas.tsx onDrop), or the card the agent makes is a second-class
	// one the renderer has to special-case.
	op := createOp(a)
	content, _ := op.Changes["content"].(map[string]any)
	if op.Changes["type"] != string(domain.TypeFile) {
		t.Errorf("compiled type = %v, want FILE", op.Changes["type"])
	}
	for _, key := range []string{"url", "attachmentId", "filename", "mimeType", "size"} {
		if content[key] == nil || content[key] == "" {
			t.Errorf("compiled FILE content is missing %q: %v", key, content)
		}
	}
	if content["mimeType"] != "application/pdf" {
		t.Errorf("mimeType = %v", content["mimeType"])
	}
	if content["filename"] != "shooting-schedule.pdf" {
		t.Errorf("filename = %v", content["filename"])
	}
	// And it does NOT carry the image shape, which is what made the card render
	// as a picture in the first place.
	if _, ok := content["caption"]; ok {
		t.Errorf("a FILE card carries an image caption: %v", content)
	}
}

// Only files attached to THIS request. An arbitrary id is somebody else's
// upload, and resolving one would let a run reach a file its person never
// offered it.
func TestAttachment_OnlyWhatWasAttachedToThisRequest(t *testing.T) {
	s := attachStaging("att-1")

	out := s.runPlaceFile(context.Background(),
		&toolArgs{AttachmentID: "att-other", ParentID: "b1"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("placed an attachment that was never offered to this run")
	}
	// And the refusal names what IS available, so a wrong id is a correction
	// rather than a dead end.
	if !strings.Contains(out.Content, "att-1") {
		t.Errorf("the refusal does not say what is attached: %s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Error("something was staged despite the refusal")
	}
}

// A file still uploading has no URL, and a card pointing at nothing is worse
// than no card.
func TestAttachment_WaitsForTheUpload(t *testing.T) {
	s := attachStaging("att-pending")
	out := s.runPlaceFile(context.Background(),
		&toolArgs{AttachmentID: "att-pending", ParentID: "b1"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("placed an attachment that has not finished uploading")
	}
}

// The caption falls back to the filename — an image card with no label is one
// nothing can refer to later.
func TestAttachment_FallsBackToTheFilename(t *testing.T) {
	s := attachStaging("att-1")
	s.runPlaceFile(context.Background(),
		&toolArgs{AttachmentID: "att-1", ParentID: "b1"}, &reply{staging: s})

	if got := s.plan.Actions[0].Title; got != "image.png" {
		t.Errorf("caption = %q, want the filename", got)
	}
}

// Containment applies here like everywhere else: a card cannot hold an image.
func TestAttachment_RespectsContainment(t *testing.T) {
	s := attachStaging("att-1")
	s.scope.Elements["card-1"] = &domain.Element{ID: "card-1", Type: domain.TypeCard}

	out := s.runPlaceFile(context.Background(),
		&toolArgs{AttachmentID: "att-1", ParentID: "card-1"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("an image was placed inside a card")
	}
}
