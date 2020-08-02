package memory

import (
	"errors"
	"io/ioutil"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	sequpdate "github.com/foxcpp/go-imap-sequpdate"
)

type User struct {
	username  string
	password  string
	mailboxes map[string]*Mailbox
	mngr 	  *sequpdate.Manager
}

func (u *User) Username() string {
	return u.username
}

func (u *User) ListMailboxes(subscribed bool) (info []imap.MailboxInfo, err error) {
	for _, mailbox := range u.mailboxes {
		if subscribed && !mailbox.Subscribed {
			continue
		}

		mboxInfo, err := mailbox.Info()
		if err != nil {
			return nil, err
		}
		info = append(info, *mboxInfo)
	}
	return
}

func (u *User) GetMailbox(name string, readOnly bool, conn backend.Conn) (*imap.MailboxStatus, backend.Mailbox, error) {
	mailbox, ok := u.mailboxes[name]
	if !ok {
		return nil, nil, backend.ErrNoSuchMailbox
	}

	status, err := u.Status(name, []imap.StatusItem{
		imap.StatusMessages, imap.StatusRecent, imap.StatusUnseen,
		imap.StatusUidNext, imap.StatusUidValidity,
	})
	if err != nil {
		return nil, nil, err
	}
	
	var (
		uids []uint32
		recent imap.SeqSet
	)
	mailbox.MessagesLock.Lock()
	for _, m := range mailbox.Messages {
		uids = append(uids, m.Uid)
		if m.Recent {
			recent.AddNum(m.Uid)
			m.Recent = false
		}
	}
	mailbox.MessagesLock.Unlock()
	
	selected := &SelectedMailbox{
		Mailbox:  mailbox,
		conn:     conn,
		readOnly: readOnly,
	}
	
	handle, err := u.mngr.Mailbox(u.username+"\x00"+name, selected, uids, &recent)
	if err != nil {
		return nil, nil, err
	}
	selected.handle = handle

	return status, selected, nil
}

func (u *User) Status(name string, items []imap.StatusItem) (*imap.MailboxStatus, error) {
	mbox, ok := u.mailboxes[name]
	if !ok {
		return nil, backend.ErrNoSuchMailbox
	}

	status := imap.NewMailboxStatus(mbox.name, items)
	status.Flags = mbox.flags()
	status.PermanentFlags = []string{"\\*"}
	status.PermanentFlags = append(status.PermanentFlags, status.Flags...)
	status.UnseenSeqNum = mbox.unseenSeqNum()

	for _, name := range items {
		switch name {
		case imap.StatusMessages:
			status.Messages = uint32(len(mbox.Messages))
		case imap.StatusUidNext:
			status.UidNext = atomic.LoadUint32(&mbox.lastUid) + 1
		case imap.StatusUidValidity:
			status.UidValidity = mbox.uidValidity
		case imap.StatusRecent:
			mbox.MessagesLock.RLock()
			for _, msg := range mbox.Messages {
				if msg.Recent {
					status.Recent++
				}
			}
			mbox.MessagesLock.RUnlock()
		case imap.StatusUnseen:
			status.Unseen = mbox.unseenSeqNum()
		}
	}

	return status, nil
}

func (u *User) SetSubscribed(name string, subscribed bool) error {
	mbox, ok := u.mailboxes[name]
	if !ok {
		return backend.ErrNoSuchMailbox
	}

	mbox.Subscribed = subscribed
	return nil
}

func (u *User) CreateMessage(mboxName string, flags []string, date time.Time, body imap.Literal) error {
	mbox, ok := u.mailboxes[mboxName]
	if !ok {
		return backend.ErrNoSuchMailbox
	}

	newFlags := flags[:0]
	for _, f := range flags {
		if f == imap.RecentFlag {
			continue
		}
		newFlags = append(newFlags, f)
	}
	flags = newFlags

	if date.IsZero() {
		date = time.Now()
	}

	b, err := ioutil.ReadAll(body)
	if err != nil {
		return err
	}

	mbox.MessagesLock.Lock()
	defer mbox.MessagesLock.Unlock()
	
	msg := &Message{
		Uid:   mbox.uidNext(),
		Date:  date,
		Size:  uint32(len(b)),
		Flags: flags,
		Body:  b,
	}
	mbox.Messages = append(mbox.Messages, msg)
	
	if u.mngr.NewMessage(u.username+"\x00"+mboxName, msg.Uid) {
		msg.Recent = true
	}
	return nil
}

func (u *User) CreateMailbox(name string) error {
	if _, ok := u.mailboxes[name]; ok {
		return backend.ErrMailboxAlreadyExists
	}

	name = strings.TrimSuffix(name, Delimiter)
	parts := strings.Split(name, Delimiter)
	mboxName := ""
	for i, p := range parts {
		mboxName += p
		_, exists := u.mailboxes[mboxName]
		if !exists {
			u.mailboxes[mboxName] = &Mailbox{
				name: mboxName, 
				user: u,
				uidValidity: uint32(rand.Int31()),
			}
		}
		if i != len(parts)-1 {
			mboxName += Delimiter
		}
	}
	
	return nil
}

func (u *User) DeleteMailbox(name string) error {
	if name == "INBOX" {
		return errors.New("Cannot delete INBOX")
	}
	if _, ok := u.mailboxes[name]; !ok {
		return backend.ErrNoSuchMailbox
	}

	delete(u.mailboxes, name)
	
	u.mngr.MailboxDestroyed(u.username+"\x00"+name)
	
	return nil
}

func (u *User) RenameMailbox(existingName, newName string) error {
	mbox, ok := u.mailboxes[existingName]
	if !ok {
		return backend.ErrNoSuchMailbox
	}
	
	for _, mbox := range u.mailboxes {
		if strings.HasPrefix(mbox.name, existingName) {
			newNameChild := strings.Replace(mbox.name, existingName, newName, 1)
			u.mailboxes[newNameChild] = &Mailbox{
				name:     newNameChild,
				Messages: mbox.Messages,
				user:     u,
			}
			u.mngr.MailboxDestroyed(u.username+"\x00"+mbox.name)
		}
	}

	mbox.Messages = nil
	if existingName != "INBOX" {
		delete(u.mailboxes, existingName)
	}

	return nil
}

func (u *User) Logout() error {
	return nil
}
