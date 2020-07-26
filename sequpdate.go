package sequpdate

import (
	"sync"

	"github.com/emersion/go-imap/backend"
)

type Manager struct{
	handlesLock sync.Mutex
	handles map[string]*sharedHandle
}

func NewManager() *Manager {
	return &Manager{
		handles:     make(map[string]*sharedHandle),
	}
}

type Mailbox interface {
	backend.Mailbox
	Conn() backend.Conn
	UIDs() ([]uint32, error)
}

func (m *Manager) Mailbox(username, mboxName string, mbox Mailbox) (*MailboxHandle, error) {
	key := username+"\x00"+mboxName
	
	m.handlesLock.Lock()
	defer m.handlesLock.Unlock()

	sharedHndl, ok := m.handles[key]
	if sharedHndl == nil {
		sharedHndl = &sharedHandle{
			key: key,
			conns: map[backend.Conn]struct{}{},
			expunged: make(chan uint32, 20),
			created: make(chan uint32, 20),
			flagsUpdated: make(chan flagsUpdate, 20),
		}
	}
	
	handle := &MailboxHandle{
		m: m,
		shared: sharedHndl,
		conn: mbox.Conn(),
		pendingFlags: make([]flagsUpdate, 0, 1),
		pendingCreated: make([]uint32, 0, 1),
	}

	if !ok {
		m.handles[key] = sharedHndl
	}

	go handle.listenUpdates()
	
	return handle, nil
}
