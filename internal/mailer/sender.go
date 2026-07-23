package mailer

import "context"

type Message struct {
	To       string
	Subject  string
	TextBody string
	HTMLBody string
}

type Sender interface {
	Send(context.Context, Message) error
}
