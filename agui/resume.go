package agui

import (
	"fmt"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

// resumeEntriesToConfirmationContent converts AG-UI resume entries into a single
// ADK user [genai.Content] containing one FunctionResponse part per entry.
//
// ADK's runner accepts user messages with FunctionResponse parts and routes them
// to the agent that issued the matching FunctionCall (see runner.handleUserFunctionCallResponse).
// Each resume entry must use interruptId equal to the adk_request_confirmation call id
// emitted in the interrupting run (see emitInterrupt).
//
// This is the resume half of the AG-UI interrupt protocol; validation against
// pending session state and response schemas is handled separately by
// [validateResumeAgainstPending] before this function is called.
//
// Non-tool resume paths (AG-UI reasons input_required and confirmation) are not
// implemented; see the TODO(non-tool-interrupts) in stream.go.
func resumeEntriesToConfirmationContent(entries []types.ResumeEntry) (*genai.Content, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("resume entries must not be empty")
	}

	parts := make([]*genai.Part, 0, len(entries))
	for i, entry := range entries {
		if entry.InterruptID == "" {
			return nil, fmt.Errorf("resume[%d]: interruptId is required", i)
		}

		response, err := confirmationResponseFromResumeEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("resume[%d]: %w", i, err)
		}

		// ADK expects the response id to match the confirmation FunctionCall id.
		parts = append(parts, &genai.Part{
			FunctionResponse: &genai.FunctionResponse{
				Name:     toolconfirmation.FunctionCallName,
				ID:       entry.InterruptID,
				Response: response,
			},
		})
	}

	return genai.NewContentFromParts(parts, genai.RoleUser), nil
}

// confirmationResponseFromResumeEntry maps one AG-UI ResumeEntry to the JSON
// object ADK reads from a toolconfirmation FunctionResponse (confirmed + optional payload).
//
// AG-UI uses payload.approved; ADK uses response.confirmed per toolconfirmation package docs.
// AG-UI editedArgs (approve-with-edits) is passed as response.payload for the tool layer.
func confirmationResponseFromResumeEntry(entry types.ResumeEntry) (map[string]any, error) {
	switch entry.Status {
	case types.ResumeStatusCancelled:
		// Cancelled means the user abandoned the prompt; treat as denial with no payload.
		return map[string]any{"confirmed": false}, nil

	case types.ResumeStatusResolved:
		payload, err := resumePayloadMap(entry.Payload)
		if err != nil {
			return nil, err
		}
		approved, ok := payload["approved"].(bool)
		if !ok {
			return nil, fmt.Errorf("resolved resume requires payload.approved (bool)")
		}

		response := map[string]any{"confirmed": approved}
		if editedArgs, ok := payload["editedArgs"]; ok {
			// Full replacement of tool args per AG-UI approve-with-edits semantics.
			response["payload"] = editedArgs
		} else if len(payload) > 1 {
			// Pass through any extra fields (excluding approved) as ADK payload context.
			extra := make(map[string]any, len(payload)-1)
			for k, v := range payload {
				if k == "approved" {
					continue
				}
				extra[k] = v
			}
			if len(extra) > 0 {
				response["payload"] = extra
			}
		}
		return response, nil

	default:
		return nil, fmt.Errorf("unsupported resume status %q", entry.Status)
	}
}

// resumePayloadMap extracts the resume payload object for a resolved entry.
// AG-UI allows payload to be any JSON value; we require an object for tool confirmations.
func resumePayloadMap(payload any) (map[string]any, error) {
	if payload == nil {
		return nil, fmt.Errorf("resolved resume requires a payload")
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload must be a JSON object, got %T", payload)
	}
	return m, nil
}
