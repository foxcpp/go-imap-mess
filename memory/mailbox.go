package memory

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/backendutil"
	sequpdate "github.com/foxcpp/go-imap-mess"
)

var Delimiter = "."

type Mailbox struct {
	Subscribed   bool
	MessagesLock sync.RWMutex
	Messages     []*Message

	lastUid     uint32
	uidValidity uint32

	name string
	user *User
}

type SelectedMailbox struct {
	*Mailbox
	conn     backend.Conn
	readOnly bool
	handle   *sequpdate.MailboxHandle
}

func (mbox *Mailbox) Name() string {
	return mbox.name
}

func (mbox *Mailbox) Info() (*imap.MailboxInfo, error) {
	info := &imap.MailboxInfo{
		Delimiter: Delimiter,
		Name:      mbox.name,
	}
	hasChildren := false
	for _, oMbox := range mbox.user.mailboxes {
		if strings.HasPrefix(oMbox.name, mbox.name+".") {
			hasChildren = true
		}
	}
	if hasChildren {
		info.Attributes = append(info.Attributes, imap.HasChildrenAttr)
	} else {
		info.Attributes = append(info.Attributes, imap.HasNoChildrenAttr)
	}
	return info, nil
}

func (mbox *Mailbox) uidNext() uint32 {
	return atomic.AddUint32(&mbox.lastUid, 1)
}

func (mbox *SelectedMailbox) Conn() backend.Conn {
	return mbox.conn
}

func (mbox *Mailbox) flags() []string {
	mbox.MessagesLock.RLock()
	defer mbox.MessagesLock.RUnlock()

	flagsMap := make(map[string]bool)
	for _, msg := range mbox.Messages {
		for _, f := range msg.Flags {
			if !flagsMap[f] {
				flagsMap[f] = true
			}
		}
	}

	var flags []string
	for f := range flagsMap {
		flags = append(flags, f)
	}
	return flags
}

func (mbox *Mailbox) unseenSeqNum() uint32 {
	mbox.MessagesLock.RLock()
	defer mbox.MessagesLock.RUnlock()

	for i, msg := range mbox.Messages {
		seqNum := uint32(i + 1)

		seen := false
		for _, flag := range msg.Flags {
			if flag == imap.SeenFlag {
				seen = true
				break
			}
		}

		if !seen {
			return seqNum
		}
	}
	return 0
}

func (mbox *SelectedMailbox) Poll(expunge bool) error {
	mbox.handle.Sync(expunge)
	return nil
}

func (mbox *SelectedMailbox) Idle(done <-chan struct{}) {
	mbox.handle.Idle(done)
}

func (mbox *SelectedMailbox) ListMessages(uid bool, seqSet *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	shouldSetSeen := false
	for _, item := range items {
		sect, err := imap.ParseBodySectionName(item)
		if err != nil {
			continue
		}
		if !sect.Peek {
			shouldSetSeen = true
		}
	}

	if shouldSetSeen {
		mbox.MessagesLock.Lock()
		defer mbox.MessagesLock.Unlock()
	} else {
		mbox.MessagesLock.RLock()
		defer mbox.MessagesLock.RUnlock()
	}

	defer mbox.handle.Sync(false)
	defer close(ch)

	seqSet, err := mbox.handle.ResolveSeq(uid, seqSet)
	if err != nil {
		if uid {
			return nil
		}
		return err
	}

	for _, msg := range mbox.Messages {
		if !seqSet.Contains(msg.Uid) {
			continue
		}

		seq, ok := mbox.handle.UidAsSeq(msg.Uid)
		if !ok {
			continue
		}

		if shouldSetSeen {
			hasSeen := false
			for _, f := range msg.Flags {
				if f == imap.SeenFlag {
					hasSeen = true
				}
			}
			if !hasSeen {
				msg.Flags = append(msg.Flags, imap.SeenFlag)
			}
			mbox.handle.FlagsChanged(msg.Uid, msg.Flags, false)
		}

		m, err := msg.Fetch(seq, items, mbox.handle.IsRecent(msg.Uid))
		if err != nil {
			continue
		}

		ch <- m
	}

	return nil
}

func (mbox *SelectedMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	mbox.MessagesLock.RLock()
	defer mbox.MessagesLock.RUnlock()

	mbox.handle.ResolveCriteria(criteria)

	defer mbox.handle.Sync(uid)

	var ids []uint32
	for _, msg := range mbox.Messages {
		seq, ok := mbox.handle.UidAsSeq(msg.Uid)
		if !ok {
			continue
		}

		ok, err := msg.Match(seq, criteria, mbox.handle.IsRecent(msg.Uid))
		if err != nil || !ok {
			continue
		}

		var id uint32
		if uid {
			id = msg.Uid
		} else {
			id = seq
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (mbox *SelectedMailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, op imap.FlagsOp, silent bool, flags []string) error {
	newFlags := flags[:0]
	for _, f := range flags {
		if f == imap.RecentFlag {
			continue
		}
		newFlags = append(newFlags, f)
	}
	flags = newFlags

	mbox.MessagesLock.RLock()
	defer mbox.MessagesLock.RUnlock()

	defer mbox.handle.Sync(uid)

	seqset, err := mbox.handle.ResolveSeq(uid, seqset)
	if err != nil {
		if uid {
			return nil
		}
		return err
	}

	for _, msg := range mbox.Messages {
		if !seqset.Contains(msg.Uid) {
			continue
		}

		msg.Flags = backendutil.UpdateFlags(msg.Flags, op, flags)
		mbox.handle.FlagsChanged(msg.Uid, msg.Flags, silent)
	}

	return nil
}

func (mbox *SelectedMailbox) CopyMessages(uid bool, seqset *imap.SeqSet, destName string) error {
	dest, ok := mbox.user.mailboxes[destName]
	if !ok {
		return backend.ErrNoSuchMailbox
	}
	destKey := mbox.user.username + "\x00" + destName

	mbox.MessagesLock.RLock()
	defer mbox.MessagesLock.RUnlock()

	defer mbox.handle.Sync(true)

	dest.MessagesLock.Lock()
	defer dest.MessagesLock.Unlock()

	seqset, err := mbox.handle.ResolveSeq(uid, seqset)
	if err != nil {
		if uid {
			return nil
		}
		return err
	}

	for _, msg := range mbox.Messages {
		if !seqset.Contains(msg.Uid) {
			continue
		}

		msgCopy := *msg
		msgCopy.Uid = dest.uidNext()

		if mbox.user.mngr.NewMessage(destKey, msgCopy.Uid) {
			msgCopy.Recent = true
		}

		dest.Messages = append(dest.Messages, &msgCopy)
	}

	return nil
}

func (mbox *SelectedMailbox) Expunge() error {
	mbox.MessagesLock.Lock()
	defer mbox.MessagesLock.Unlock()

	for i := len(mbox.Messages) - 1; i >= 0; i-- {
		msg := mbox.Messages[i]

		deleted := false
		for _, flag := range msg.Flags {
			if flag == imap.DeletedFlag {
				deleted = true
				break
			}
		}

		if deleted {
			mbox.Messages = append(mbox.Messages[:i], mbox.Messages[i+1:]...)
			mbox.handle.Removed(msg.Uid)
		}
	}

	mbox.handle.Sync(true)

	return nil
}

func (mbox *SelectedMailbox) Close() error {
	return mbox.handle.Close()
}
