package mess

import (
	"errors"
	"sync"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
)

type flagsUpdate struct {
	uid      uint32
	newFlags []string
}

type sharedHandle struct {
	key interface{}

	handlesLock sync.RWMutex
	handles     map[*MailboxHandle]struct{}
}

type MailboxHandle struct {
	m      *Manager
	shared *sharedHandle
	conn   backend.Conn

	lock           sync.RWMutex
	idleerNotify   chan struct{}
	uidMap         []uint32
	recent         *imap.SeqSet
	hasNewRecent   bool
	recentCount    uint32
	pendingExpunge imap.SeqSet
	pendingCreated imap.SeqSet
	pendingFlags   []flagsUpdate
}

var ErrNoMessages = errors.New("No messages matched")

// ResolveSeq converts the passed UIDs or sequence numbers set into UIDs set
// that is appropriate for mailbox operations in this connection.
//
// If resolution algorithm results in an empty set, ErrNoMessages is
// returned.
// Resulting set *may* include UIDs that were expunged in other
// connections, backend should ignore these as specified in RFC 3501.
func (handle *MailboxHandle) ResolveSeq(uid bool, set *imap.SeqSet) (*imap.SeqSet, error) {
	handle.lock.RLock()
	defer handle.lock.RUnlock()

	if len(handle.uidMap) == 0 {
		return &imap.SeqSet{}, ErrNoMessages
	}

	if uid {
		for i, seq := range set.Set {
			if seq.Start == 0 {
				seq.Start = handle.uidMap[len(handle.uidMap)-1]
			}
			if seq.Stop == 0 {
				seq.Stop = handle.uidMap[len(handle.uidMap)-1]
			}

			// Resolving certain UID sets may yield cases in which
			// start value is bigger than stop. However, as opposed to
			// seqnum sets, this is a valid and meaningful set
			// that may be passed to backend as go-imap cannot sort it
			// meaningfully.
			//
			// E.g. UIDNEXT:*  should be basically equivalent to *
			// and refer to the last message.
			if seq.Start > seq.Stop {
				seq.Start, seq.Stop = seq.Stop, seq.Start
			}

			set.Set[i] = seq
		}

		return set, nil
	}

	result := &imap.SeqSet{}
	for _, seq := range set.Set {
		seq, ok := seqToUid(handle.uidMap, seq)
		if !ok {
			continue
		}
		result.AddRange(seq.Start, seq.Stop)
	}

	if len(result.Set) == 0 {
		return &imap.SeqSet{}, ErrNoMessages
	}

	return result, nil
}

// ResolveCriteria converts all SeqNum rules into corresponding Uid
// rules. Argument is modified directly.
func (handle *MailboxHandle) ResolveCriteria(criteria *imap.SearchCriteria) {
	if criteria.Uid != nil {
		seq, _ := handle.ResolveSeq(true, criteria.Uid)
		criteria.Uid = seq
	}
	if criteria.SeqNum != nil {
		if criteria.Uid == nil {
			criteria.Uid = new(imap.SeqSet)
		}
		seq, _ := handle.ResolveSeq(false, criteria.SeqNum)
		criteria.Uid.AddSet(seq)
		criteria.SeqNum = nil
	}

	for _, not := range criteria.Not {
		handle.ResolveCriteria(not)
	}
	for _, or := range criteria.Or {
		handle.ResolveCriteria(or[0])
		handle.ResolveCriteria(or[1])
	}
}

func (handle *MailboxHandle) UidAsSeq(uid uint32) (uint32, bool) {
	handle.lock.RLock()
	defer handle.lock.RUnlock()

	seq, ok := uidToSeq(handle.uidMap, imap.Seq{Start: uid, Stop: uid})
	return seq.Start, ok
}

func (handle *MailboxHandle) Idle(done <-chan struct{}) {
	handle.lock.Lock()
	handle.idleerNotify = make(chan struct{}, 1)
	handle.lock.Unlock()

	defer func() {
		handle.lock.Lock()
		handle.idleerNotify = nil
		handle.lock.Unlock()
	}()

	for {
		select {
		case <-handle.idleerNotify:
			handle.Sync(true)
		case <-done:
			return
		}
	}
}

// Sync sends all updates pending for this connection.
// This method should be called after each mailbox operation to
// ensure client sees changes as early as possible.
//
// expunge should be set to true if EXPUNGE updates should be
// sent. IT SHOULD NOT BE SET WHILE EXECUTING A COMMAND
// USING SEQUENCE NUMBERS (except for COPY).
func (handle *MailboxHandle) Sync(expunge bool) {
	handle.lock.Lock()
	defer handle.lock.Unlock()

	handle.syncUnlocked(expunge)
}

func (handle *MailboxHandle) syncUnlocked(expunge bool) {
	handle.lock.Lock()
	defer handle.lock.Unlock()

	for _, upd := range handle.pendingFlags {
		seq, ok := uidToSeq(handle.uidMap, imap.Seq{Start: upd.uid, Stop: upd.uid})
		if !ok {
			// Likely the corresponding message was expunged.
			continue
		}
		updMsg := imap.NewMessage(seq.Start, []imap.FetchItem{imap.FetchFlags, imap.FetchUid})
		updMsg.Flags = upd.newFlags

		updMsg.Uid = upd.uid
		handle.conn.SendUpdate(&backend.MessageUpdate{
			Message: updMsg,
		})
	}
	handle.pendingFlags = make([]flagsUpdate, 0, 1)

	if expunge && !handle.pendingExpunge.Empty() {
		expunged := make([]uint32, 0, 16)
		newMap := handle.uidMap[:0] /* SliceTricks: filtering without allocations */
		for i, uid := range handle.uidMap {
			if handle.pendingExpunge.Contains(uid) {
				expunged = append(expunged, uint32(i+1))
				continue
			}
			newMap = append(newMap, uid)
		}
		handle.uidMap = newMap

		for i := len(expunged) - 1; i >= 0; i-- {
			handle.conn.SendUpdate(&backend.ExpungeUpdate{SeqNum: expunged[i]})
		}
	}

	if !handle.pendingCreated.Empty() {
		for _, seq := range handle.pendingCreated.Set {
			for i := seq.Start; i <= seq.Stop; i++ {
				handle.uidMap = append(handle.uidMap, i)
			}
		}
		handle.pendingCreated.Clear()

		status := imap.NewMailboxStatus("", []imap.StatusItem{imap.StatusMessages})
		status.Messages = uint32(len(handle.uidMap))
		handle.conn.SendUpdate(&backend.MailboxUpdate{
			MailboxStatus: status,
		})

		// Order in which go-imap sends separate MailboxUpdate elements
		// is non-deterministic and depend son Items map order.
		//
		// However, imaptest wants to have RECENT always after EXISTS
		// and I believe it may indeed cause trouble for some clients
		// so we work-around it by sending multiple separate update objects.
		if handle.hasNewRecent {
			status.Items = map[imap.StatusItem]interface{}{
				imap.StatusRecent: nil,
			}
			status.Recent = handle.recentCount
			handle.hasNewRecent = true
			handle.conn.SendUpdate(&backend.MailboxUpdate{
				MailboxStatus: status,
			})
		}
	}
}

func (handle *MailboxHandle) enqueueFlagsUpdate(uid uint32, newFlags []string) {
	upd := flagsUpdate{
		uid:      uid,
		newFlags: newFlags,
	}

	handle.lock.Lock()
	if handle.recent.Contains(uid) {
		upd.newFlags = make([]string, len(newFlags))
		copy(upd.newFlags, newFlags)
		upd.newFlags = append(upd.newFlags, imap.RecentFlag)
	}

	exists := false
	for i, upd := range handle.pendingFlags {
		if upd.uid == uid {
			handle.pendingFlags[i].newFlags = upd.newFlags
			exists = true
			break
		}
	}
	if !exists {
		handle.pendingFlags = append(handle.pendingFlags, upd)
	}

	handle.idleUpdate()
	handle.lock.Unlock()
}

// FlagsChanged performans all necessary update dispatching
// actions on flags change.
//
// newFlags should not include \Recent, silent should be set
// if UpdateMessagesFlags was called with it set.
func (handle *MailboxHandle) FlagsChanged(uid uint32, newFlags []string, silent bool) {
	handle.shared.handlesLock.RLock()
	defer handle.shared.handlesLock.RUnlock()

	for hndl := range handle.shared.handles {
		if hndl == handle && silent {
			continue
		}

		hndl.enqueueFlagsUpdate(uid, newFlags)
	}
}

// IsRecent indicates whether the message should be considered
// to have \Recent flag for this connection.
func (handle *MailboxHandle) IsRecent(uid uint32) bool {
	handle.lock.RLock()
	defer handle.lock.RUnlock()
	return handle.recent.Contains(uid)
}

func (handle *MailboxHandle) idleUpdate() {
	if handle.idleerNotify != nil {
		select {
		case handle.idleerNotify <- struct{}{}:
		default:
		}
	}
}

// Removed performs all necessary update dispatching actions
// for a specified removed message.
func (handle *MailboxHandle) Removed(uid uint32) {
	handle.shared.handlesLock.RLock()
	defer handle.shared.handlesLock.RUnlock()

	for hndl := range handle.shared.handles {
		hndl.lock.Lock()
		hndl.pendingExpunge.AddNum(uid)
		hndl.idleUpdate()
		hndl.lock.Unlock()
	}
}

func (handle *MailboxHandle) RemovedSet(seq imap.SeqSet) {
	handle.shared.handlesLock.RLock()
	defer handle.shared.handlesLock.RUnlock()

	for hndl := range handle.shared.handles {
		hndl.lock.Lock()
		hndl.pendingExpunge.AddSet(&seq)
		hndl.idleUpdate()
		hndl.lock.Unlock()
	}
}

func (handle *MailboxHandle) MsgsCount() int {
	return len(handle.uidMap)
}

func (handle *MailboxHandle) Close() error {
	handle.m.handlesLock.Lock()
	defer handle.m.handlesLock.Unlock()

	handle.shared.handlesLock.Lock()
	defer handle.shared.handlesLock.Unlock()

	delete(handle.shared.handles, handle)

	if len(handle.shared.handles) == 0 {
		delete(handle.m.handles, handle.shared.key)
	}

	return nil
}
