package auth

import "context"

type Store interface {
	Load(context.Context, string) (Credential, error)
	Save(context.Context, string, Credential) error
	Delete(context.Context, string) error
}
