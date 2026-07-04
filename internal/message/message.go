package message

import sdk "github.com/anthropics/anthropic-sdk-go"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

type Message = sdk.MessageParam
type ContentBlock = sdk.ContentBlockParamUnion

type History struct {
	Messages []sdk.MessageParam
}

func NewUser(text string) sdk.MessageParam {
	return sdk.NewUserMessage(sdk.NewTextBlock(text))
}

func NewAssistant(blocks ...sdk.ContentBlockParamUnion) sdk.MessageParam {
	return sdk.NewAssistantMessage(blocks...)
}
