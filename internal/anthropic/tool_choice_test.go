package anthropic

import (
	"encoding/json"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

func TestForcedToolChoiceAcrossProtocols(t *testing.T) {
	req := MessageRequest{Model: "chat", MaxTokens: 1024, ForceTool: "compress", Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("compress history"))}, Tools: []sdk.ToolUnionParam{{OfTool: &sdk.ToolParam{Name: "compress", InputSchema: sdk.ToolInputSchemaParam{Properties: map[string]any{}}}}}}
	for _, format := range []string{"anthropic", "sdk", "openai", "responses"} {
		t.Run(format, func(t *testing.T) {
			var body any
			switch format {
			case "anthropic":
				body = req
			case "sdk":
				body = req.ToSDKParams()
			case "openai":
				var err error
				body, err = buildOpenAIRequest(req)
				if err != nil {
					t.Fatal(err)
				}
			case "responses":
				var err error
				body, err = buildResponsesRequest(req)
				if err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			choice, ok := wire["tool_choice"].(map[string]any)
			if !ok {
				t.Fatal("missing forced tool_choice")
			}
			if format == "anthropic" || format == "sdk" {
				if choice["type"] != "tool" || choice["name"] != "compress" || choice["disable_parallel_tool_use"] != true {
					t.Fatal(choice)
				}
			} else {
				if choice["type"] != "function" || wire["parallel_tool_calls"] != false {
					t.Fatal(wire)
				}
				if format == "openai" {
					if choice["function"].(map[string]any)["name"] != "compress" {
						t.Fatal(choice)
					}
				} else if choice["name"] != "compress" {
					t.Fatal(choice)
				}
			}
		})
	}
}
