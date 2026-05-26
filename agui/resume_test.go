package agui

import (
	"testing"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

func TestResumeEntriesToConfirmationContent(t *testing.T) {
	t.Run("resolved approved", func(t *testing.T) {
		content, err := resumeEntriesToConfirmationContent([]types.ResumeEntry{{
			InterruptID: "confirm-1",
			Status:      types.ResumeStatusResolved,
			Payload:     map[string]any{"approved": true},
		}})
		if err != nil {
			t.Fatalf("resumeEntriesToConfirmationContent() error = %v", err)
		}
		assertConfirmationPart(t, content, "confirm-1", map[string]any{"confirmed": true})
	})

	t.Run("resolved denied", func(t *testing.T) {
		content, err := resumeEntriesToConfirmationContent([]types.ResumeEntry{{
			InterruptID: "confirm-1",
			Status:      types.ResumeStatusResolved,
			Payload:     map[string]any{"approved": false},
		}})
		if err != nil {
			t.Fatalf("resumeEntriesToConfirmationContent() error = %v", err)
		}
		assertConfirmationPart(t, content, "confirm-1", map[string]any{"confirmed": false})
	})

	t.Run("resolved with editedArgs", func(t *testing.T) {
		edited := map[string]any{"to": "b@c.com"}
		content, err := resumeEntriesToConfirmationContent([]types.ResumeEntry{{
			InterruptID: "confirm-1",
			Status:      types.ResumeStatusResolved,
			Payload: map[string]any{
				"approved":   true,
				"editedArgs": edited,
			},
		}})
		if err != nil {
			t.Fatalf("resumeEntriesToConfirmationContent() error = %v", err)
		}
		assertConfirmationPart(t, content, "confirm-1", map[string]any{
			"confirmed": true,
			"payload":   edited,
		})
	})

	t.Run("cancelled", func(t *testing.T) {
		content, err := resumeEntriesToConfirmationContent([]types.ResumeEntry{{
			InterruptID: "confirm-2",
			Status:      types.ResumeStatusCancelled,
		}})
		if err != nil {
			t.Fatalf("resumeEntriesToConfirmationContent() error = %v", err)
		}
		assertConfirmationPart(t, content, "confirm-2", map[string]any{"confirmed": false})
	})

	t.Run("multiple entries", func(t *testing.T) {
		content, err := resumeEntriesToConfirmationContent([]types.ResumeEntry{
			{
				InterruptID: "confirm-a",
				Status:      types.ResumeStatusResolved,
				Payload:     map[string]any{"approved": true},
			},
			{
				InterruptID: "confirm-b",
				Status:      types.ResumeStatusCancelled,
			},
		})
		if err != nil {
			t.Fatalf("resumeEntriesToConfirmationContent() error = %v", err)
		}
		if len(content.Parts) != 2 {
			t.Fatalf("len(parts) = %d, want 2", len(content.Parts))
		}
	})

	t.Run("empty entries", func(t *testing.T) {
		_, err := resumeEntriesToConfirmationContent(nil)
		if err == nil {
			t.Fatal("expected error for empty resume")
		}
	})

	t.Run("missing interruptId", func(t *testing.T) {
		_, err := resumeEntriesToConfirmationContent([]types.ResumeEntry{{
			Status:  types.ResumeStatusCancelled,
		}})
		if err == nil {
			t.Fatal("expected error for missing interruptId")
		}
	})

	t.Run("resolved missing approved", func(t *testing.T) {
		_, err := resumeEntriesToConfirmationContent([]types.ResumeEntry{{
			InterruptID: "confirm-1",
			Status:      types.ResumeStatusResolved,
			Payload:     map[string]any{},
		}})
		if err == nil {
			t.Fatal("expected error for missing approved")
		}
	})
}

func assertConfirmationPart(t *testing.T, content *genai.Content, wantID string, wantResponse map[string]any) {
	t.Helper()
	if content == nil || len(content.Parts) != 1 {
		t.Fatalf("content = %#v, want single part", content)
	}
	fr := content.Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("FunctionResponse is nil")
	}
	if fr.Name != toolconfirmation.FunctionCallName {
		t.Errorf("Name = %q, want %q", fr.Name, toolconfirmation.FunctionCallName)
	}
	if fr.ID != wantID {
		t.Errorf("ID = %q, want %q", fr.ID, wantID)
	}
	if fr.Response["confirmed"] != wantResponse["confirmed"] {
		t.Errorf("Response[confirmed] = %v, want %v", fr.Response["confirmed"], wantResponse["confirmed"])
	}
	if wantPayload, ok := wantResponse["payload"]; ok {
		gotPayload, ok := fr.Response["payload"]
		if !ok {
			t.Fatalf("Response[payload] missing, want %v", wantPayload)
		}
		gotMap, ok := gotPayload.(map[string]any)
		if !ok {
			t.Fatalf("Response[payload] type = %T, want map", gotPayload)
		}
		wantMap := wantPayload.(map[string]any)
		for k, v := range wantMap {
			if gotMap[k] != v {
				t.Errorf("Response[payload][%q] = %v, want %v", k, gotMap[k], v)
			}
		}
	} else if _, ok := fr.Response["payload"]; ok {
		t.Errorf("Response[payload] = %v, want absent", fr.Response["payload"])
	}
}
