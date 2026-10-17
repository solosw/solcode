package mcp

import (
	"encoding/base64"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

func TestMCPImageResultRemainsVisionContent(t *testing.T) {
	data := []byte("not-real-png-image-for-transport-test")
	got, err := contentBlockFromCallResult(&sdkmcp.CallToolResult{Content: []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "app accessibility tree"}, &sdkmcp.ImageContent{MIMEType: "image/png", Data: data},
	}})
	if err != nil || got.Type != "image" || got.MimeType != "image/png" || got.Data != base64.StdEncoding.EncodeToString(data) || !strings.Contains(got.Text, "accessibility tree") {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if strings.Contains(got.Text, got.Data) || strings.Contains(got.Text, "mimeType:") {
		t.Fatal("image was serialized as text")
	}
}
func TestMCPErrorImagesDoNotBecomeSuccessfulResults(t *testing.T) {
	got, err := contentBlockFromCallResult(&sdkmcp.CallToolResult{IsError: true, Content: []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "permission denied"}, &sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte("abc")},
	}})
	if err != nil || !got.IsError || got.Text != "permission denied" || got.Data != "" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
func TestMCPAdditionalImagesNotDumpedIntoText(t *testing.T) {
	got, err := contentBlockFromCallResult(&sdkmcp.CallToolResult{Content: []sdkmcp.Content{
		&sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte("first")}, &sdkmcp.ImageContent{MIMEType: "image/png", Data: []byte("second")},
	}})
	if err != nil || got.Type != "image" || !strings.Contains(got.Text, "1 additional") || strings.Contains(got.Text, base64.StdEncoding.EncodeToString([]byte("second"))) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
