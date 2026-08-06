package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// A presigned URL is a bearer credential for direct bucket access that bypasses
// the application entirely. It sat in content.url and therefore travelled into
// every `format=json` export and every "download my data" bundle — files the
// person mails to themselves, that stay valid for seven days after the share
// they came with is revoked. An export is an archive, not a set of keys.
func exportFixture(t *testing.T) (*BoardService, *memory.ElementRepo, *memory.AttachmentRepo) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	elements := memory.NewElementRepo()
	atts := memory.NewAttachmentRepo()

	mk := func(id, typ, parent string, acl *domain.ACL, content domain.Content) {
		if err := elements.Insert(ctx, &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location: domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:  content, ACL: acl,
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	mk("eeeeeeeeeeeeeeeeeeeeee01", "BOARD", "", &domain.ACL{OwnerID: "alice", Editors: []string{}},
		domain.Content{"title": "Lookbook"})
	mk("eeeeeeeeeeeeeeeeeeeeee02", "IMAGE", "eeeeeeeeeeeeeeeeeeeeee01", nil, domain.Content{
		"attachmentId": "att-still",
		"url":          "https://bucket/u/alice/att-still/still.jpg?X-Amz-Credential=AKIA&X-Amz-Signature=deadbeef",
	})
	if err := atts.Insert(ctx, &domain.Attachment{
		ID: "att-still", OwnerID: "alice", Key: "u/alice/att-still/still.jpg",
		Filename: "still.jpg", ContentType: "image/jpeg", Size: 482113,
		Status: domain.AttachmentUploaded, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	svc := NewBoardService(elements, nil, NewAccessResolver(elements))
	svc.AttachAttachments(atts)
	return svc, elements, atts
}

func TestExportJSON_CarriesAManifestRatherThanSignedURLs(t *testing.T) {
	svc, _, _ := exportFixture(t)
	body, _, err := svc.Export(context.Background(), &domain.Principal{Sub: "alice"},
		"eeeeeeeeeeeeeeeeeeeeee01", "json")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if strings.Contains(body, "X-Amz-Signature") || strings.Contains(body, "X-Amz-Credential") {
		t.Fatal("the export carries a live bucket credential; revoking the share does nothing to a file already downloaded")
	}

	var payload struct {
		Attachments []ExportedAttachment `json:"attachments"`
		Elements    []struct {
			Content map[string]any `json:"content"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("the export is not readable json: %v", err)
	}
	if len(payload.Attachments) != 1 {
		t.Fatalf("manifest = %+v, want the one file this board refers to", payload.Attachments)
	}
	got := payload.Attachments[0]
	if got.Filename != "still.jpg" || got.ContentType != "image/jpeg" || got.Size != 482113 {
		t.Errorf("manifest entry = %+v; an archive has to say what the file WAS", got)
	}
	// The reference survives — the file is still findable through the route that
	// checks whether you may see it.
	if payload.Elements[0].Content["attachmentId"] != "att-still" {
		t.Errorf("the element lost its reference too: %+v", payload.Elements[0].Content)
	}
}

func TestExportData_StripsTheCredentialsFromTheAccountArchiveToo(t *testing.T) {
	_, elements, atts := exportFixture(t)
	audit, _ := testAudit()
	svc := NewAccountService(newStubUsers(), elements, memory.NewLabelRepo(), atts,
		stubNotifications{}, nil, audit, zap.NewNop())

	export, err := svc.ExportData(context.Background(), &domain.Principal{Sub: "alice"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	body, _ := json.Marshal(export)
	if strings.Contains(string(body), "X-Amz-Signature") {
		t.Fatal("the privacy export is a set of live bucket keys")
	}
	if len(export.Attachments) != 1 || export.Attachments[0].Filename != "still.jpg" {
		t.Errorf("manifest = %+v, want the file named", export.Attachments)
	}
}
