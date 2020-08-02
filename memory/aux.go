package memory

import (
	"github.com/emersion/go-imap/backend"
)

func (b *Backend) GetUser(username string) (backend.User, error) {
	usr := b.users[username]
	if usr == nil {
		return nil, backend.ErrInvalidCredentials
	}
	return usr, nil
}

func (b *Backend) CreateUser(name string) error {
	user := &User{
		username: name,
		password: "password",
		mngr: 	  b.manager,
	}

	user.mailboxes = map[string]*Mailbox{
		"INBOX": {
			name: "INBOX",
			user: user,
			Messages: nil,
		},
	}
	
	b.users[name] = user
	return nil
}
