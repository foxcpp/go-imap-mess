package mess

import (
	"sync"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
)

type Manager struct{
	handlesLock sync.RWMutex
	handles map[interface{}]*sharedHandle
}

func NewManager() *Manager {
	return &Manager{
		handles:     make(map[interface{}]*sharedHandle),
	}
}

type Mailbox interface {
	backend.Mailbox
	Conn() backend.Conn
}

// Mailbox initializes a new message handle for the mailbox.
//
// key should be a server-global unique identifier for the mailbox.
// uids should contain the list of all message UIDs existing in the mailbox.
//
// recents should contain the list of message UIDs with persistent \Recent flag.
// Note that persistent \Recent should be unset once passed to Mailbox().
// In particular, two subsequent calls should not receive the same value.
func (m *Manager) Mailbox(key interface{}, mbox Mailbox, uids []uint32, recents *imap.SeqSet) (*MailboxHandle, error) {
	m.handlesLock.Lock()
	defer m.handlesLock.Unlock()

	sharedHndl, ok := m.handles[key]
	if sharedHndl == nil {
		sharedHndl = &sharedHandle{
			key: key,
			handles: map[*MailboxHandle]struct{}{},
		}
	}
	
	handle := &MailboxHandle{
		m:            m,
		shared:       sharedHndl,
		conn:         mbox.Conn(),
		uidMap:       uids,
		recent:       recents,
		pendingFlags: make([]flagsUpdate, 0, 1),
	}

	sharedHndl.handlesLock.Lock()
	sharedHndl.handles[handle] = struct{}{}
	sharedHndl.handlesLock.Unlock()
	if !ok {
		m.handles[key] = sharedHndl
	}
	
	return handle, nil
}

// NewMessage performs necessary updates dispatching when
// new messages are added to the mailbox.
//
// Return value indicates whether backend should store
// a persistent \Recent flag in DB for further retrieval
// (see Mailbox)
func (m *Manager) NewMessages(key interface{}, uid imap.SeqSet) (storeRecent bool) {
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
		hndl.pendingCreated.AddSet(&uid)
		if !addedRecent {
			hndl.recent.AddSet(&uid)
			for _, set := range uid.Set {
				for i := set.Start; i <= set.Stop; i++ {
					hndl.recentCount++
				}
			}
			hndl.hasNewRecent = true
			addedRecent = true
		}
		hndl.lock.Unlock()
	}
	
	return !addedRecent
}

func (m *Manager) NewMessage(key interface{}, uid uint32) (storeRecent bool) {
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
func (m *Manager) MailboxDestroyed(key interface{}) {
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
}