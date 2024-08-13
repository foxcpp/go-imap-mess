package mess

import (
	"sync"

	"github.com/emersion/go-imap/v2"
)

type Manager[MailboxKey comparable] struct {
	handlesLock sync.RWMutex
	handles     map[MailboxKey]*sharedHandle[MailboxKey]

	sink chan<- Update[MailboxKey]

	ExternalSubscribe   func(key MailboxKey)
	ExternalUnsubscribe func(key MailboxKey)
}

func NewManager[MailboxKey comparable]() *Manager[MailboxKey] {
	return &Manager[MailboxKey]{
		handles: make(map[MailboxKey]*sharedHandle[MailboxKey]),
	}
}

// ManagementHandle initializes a new message handle for the mailbox that
// is opened without an active connection, e.g. from an administrative UI or
// CLI utility.
//
// Such handle never sends updates to mbox.Conn(), only to SetExternalSink if
// set. \Recent flag for new messages will never be shown to such connections
// and it will receive no updates for mailbox changes anyway (Idle, Sync are no-op).
func (m *Manager[MailboxKey]) ManagementHandle(key MailboxKey, uids []imap.UID, recents imap.UIDSet) *MailboxHandle[MailboxKey] {
	return &MailboxHandle[MailboxKey]{
		m:          m,
		key:        key,
		management: true,
		recent:     recents,
		uidMap:     uids,
	}
}

// Mailbox initializes a new message handle for the mailbox.
//
// key should be a server-global unique identifier for the mailbox.
// uids should contain the list of all message UIDs existing in the mailbox.
//
// recents should contain the list of message UIDs with persistent \Recent flag.
// Note that persistent \Recent should be unset once passed to Mailbox().
// In particular, two subsequent calls should not receive the same value.
func (m *Manager[MailboxKey]) Mailbox(key MailboxKey, uids []imap.UID, recents imap.UIDSet) (*MailboxHandle[MailboxKey], error) {
	m.handlesLock.Lock()
	defer m.handlesLock.Unlock()

	sharedHndl, ok := m.handles[key]
	if sharedHndl == nil {
		sharedHndl = &sharedHandle[MailboxKey]{
			key:     key,
			handles: map[*MailboxHandle[MailboxKey]]struct{}{},
		}
	}

	handle := &MailboxHandle[MailboxKey]{
		m:            m,
		key:          key,
		shared:       sharedHndl,
		uidMap:       uids,
		recent:       recents,
		pendingFlags: make([]flagsUpdate, 0, 1),
	}
	for _, set := range recents {
		for i := set.Start; i <= set.Stop; i++ {
			handle.recentCount++
		}
	}

	sharedHndl.handlesLock.Lock()
	sharedHndl.handles[handle] = struct{}{}
	sharedHndl.handlesLock.Unlock()
	if !ok {
		m.handles[key] = sharedHndl
		if m.ExternalSubscribe != nil {
			m.ExternalSubscribe(key)
		}
	}

	return handle, nil
}

// NewMessages performs necessary updates dispatching when
// new messages are added to the mailbox.
//
// Return value indicates whether backend should store
// a persistent \Recent flag in DB for further retrieval
// (see Mailbox)
func (m *Manager[MailboxKey]) NewMessages(key MailboxKey, uid imap.UIDSet) (storeRecent bool) {
	if m.sink != nil {
		m.sink <- Update[MailboxKey]{
			Type:   UpdNewMessage,
			Key:    key,
			SeqSet: uid,
		}
	}

	return m.newMessages(key, uid)
}

func (m *Manager[MailboxKey]) newMessages(key MailboxKey, uid imap.UIDSet) (storeRecent bool) {
	m.handlesLock.RLock()
	defer m.handlesLock.RUnlock()

	handle := m.handles[key]
	if handle == nil {
		return false
	}

	handle.handlesLock.RLock()
	defer handle.handlesLock.RUnlock()

	addedRecent := false
	for hndl := range handle.handles {
		hndl.lock.Lock()
		hndl.pendingCreated.AddSet(uid)
		if !addedRecent {
			hndl.recent.AddSet(uid)
			for _, set := range uid {
				for i := set.Start; i <= set.Stop; i++ {
					hndl.recentCount++
				}
			}
			hndl.hasNewRecent = true
			addedRecent = true
		}
		hndl.idleUpdate()
		hndl.lock.Unlock()
	}

	return !addedRecent
}

func (m *Manager[MailboxKey]) NewMessage(key MailboxKey, uid imap.UID) (storeRecent bool) {
	if m.sink != nil {
		m.sink <- Update[MailboxKey]{
			Type:   UpdNewMessage,
			Key:    key,
			SeqSet: imap.UIDSetNum(uid),
		}
	}

	m.handlesLock.RLock()
	defer m.handlesLock.RUnlock()

	handle := m.handles[key]
	if handle == nil {
		return true
	}

	handle.handlesLock.RLock()
	defer handle.handlesLock.RUnlock()

	addedRecent := false
	for hndl := range handle.handles {
		hndl.lock.Lock()
		hndl.pendingCreated.AddNum(uid)
		if !addedRecent {
			hndl.recent.AddNum(uid)
			hndl.hasNewRecent = true
			hndl.recentCount++
			addedRecent = true
		}
		hndl.idleUpdate()
		hndl.lock.Unlock()
	}

	return !addedRecent
}

// MailboxDestroyed should be called when the specified key is no longer
// valid for the mailbox e.g. because it was renamed or deleted.
//
// The appropriate place to call the method from is
// DeleteMailbox - MailboxDestroyed should be called
// for all removed mailboxes - and RenameMailbox where
// it should be called for _both_ source and target mailbox.
//
// In all cases it is better to call MailboxDestroyed _after_
// physically deleting the mailbox.
func (m *Manager[MailboxKey]) MailboxDestroyed(key MailboxKey) {
	if m.sink != nil {
		m.sink <- Update[MailboxKey]{
			Type: UpdMboxDestroyed,
			Key:  key,
		}
	}

	m.mailboxDestroyed(key)
}

func (m *Manager[MailboxKey]) mailboxDestroyed(key MailboxKey) {
	m.handlesLock.RLock()
	defer m.handlesLock.RUnlock()

	handle := m.handles[key]
	if handle == nil {
		return
	}

	handle.handlesLock.Lock()
	handle.handles = nil
	handle.handlesLock.Unlock()

	delete(m.handles, key)

	if m.ExternalUnsubscribe != nil {
		m.ExternalUnsubscribe(key)
	}
}

func (m *Manager[MailboxKey]) removedSet(key MailboxKey, seq imap.UIDSet) {
	m.handlesLock.RLock()
	defer m.handlesLock.RUnlock()

	handle := m.handles[key]
	if handle == nil {
		return
	}

	handle.handlesLock.RLock()
	defer handle.handlesLock.RUnlock()

	for hndl := range handle.handles {
		hndl.lock.Lock()
		hndl.pendingExpunge.AddSet(seq)
		hndl.idleUpdate()
		hndl.lock.Unlock()
	}
}

func (m *Manager[MailboxKey]) flagsChanged(key MailboxKey, uid imap.UID, newFlags []imap.Flag) {
	m.handlesLock.RLock()
	defer m.handlesLock.RUnlock()

	handle := m.handles[key]
	if handle == nil {
		return
	}

	handle.handlesLock.RLock()
	defer handle.handlesLock.RUnlock()

	for hndl := range handle.handles {
		hndl.enqueueFlagsUpdate(uid, newFlags)
	}
}
