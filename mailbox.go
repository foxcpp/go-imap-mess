package mess

import (
	"errors"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

type flagsUpdate struct {
	uid      imap.UID
	newFlags []imap.Flag
}

type sharedHandle[MailboxKey comparable] struct {
	key MailboxKey

	handlesLock sync.RWMutex
	handles     map[*MailboxHandle[MailboxKey]]struct{}
}

type MailboxHandle[MailboxKey comparable] struct {
	m          *Manager[MailboxKey]
	key        MailboxKey
	shared     *sharedHandle[MailboxKey]
	management bool

	lock           sync.RWMutex
	idleerNotify   chan struct{}
	uidMap         []imap.UID
	recent         imap.UIDSet
	hasNewRecent   bool
	recentCount    uint32
	pendingExpunge imap.UIDSet
	pendingCreated imap.UIDSet
	pendingFlags   []flagsUpdate
}

var ErrNoMessages = errors.New("No messages matched")

func (handle *MailboxHandle[MailboxKey]) ResolveUID(set imap.UIDSet) (imap.UIDSet, error) {
	handle.lock.RLock()
	defer handle.lock.RUnlock()

	if len(handle.uidMap) == 0 {
		return imap.UIDSet{}, ErrNoMessages
	}

	for i, seq := range set {
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

		set[i] = seq
	}

	return set, nil
}

// ResolveSeq converts the passed UIDs or sequence numbers set into UIDs set
// that is appropriate for mailbox operations in this connection.
//
// If resolution algorithm results in an empty set, ErrNoMessages is
// returned.
// Resulting set *may* include UIDs that were expunged in other
// connections, backend should ignore these as specified in RFC 3501.
func (handle *MailboxHandle[MailboxKey]) ResolveSeq(set imap.SeqSet) (imap.UIDSet, error) {
	handle.lock.RLock()
	defer handle.lock.RUnlock()

	if len(handle.uidMap) == 0 {
		return imap.UIDSet{}, ErrNoMessages
	}

	result := imap.UIDSet{}
	for _, seq := range set {
		seq, ok := seqToUid(handle.uidMap, seq)
		if !ok {
			continue
		}
		result.AddRange(seq.Start, seq.Stop)
	}

	if len(result) == 0 {
		return imap.UIDSet{}, ErrNoMessages
	}

	return result, nil
}

// ResolveCriteria converts all SeqNum rules into corresponding Uid
// rules. Argument is modified directly.
func (handle *MailboxHandle[MailboxKey]) ResolveCriteria(criteria *imap.SearchCriteria) {
	if criteria.UID != nil {
		for i, set := range criteria.UID {
			resolved, _ := handle.ResolveUID(set)
			criteria.UID[i] = resolved
		}
	}
	if criteria.SeqNum != nil {
		for _, set := range criteria.SeqNum {
			set, _ := handle.ResolveSeq(set)
			criteria.UID = append(criteria.UID, set)
		}
		criteria.SeqNum = nil
	}

	for _, not := range criteria.Not {
		handle.ResolveCriteria(&not)
	}
	for _, or := range criteria.Or {
		handle.ResolveCriteria(&or[0])
		handle.ResolveCriteria(&or[1])
	}
}

func (handle *MailboxHandle[MailboxKey]) UidAsSeq(uid imap.UID) (uint32, bool) {
	handle.lock.RLock()
	defer handle.lock.RUnlock()

	seq, ok := uidToSeq(handle.uidMap, imap.UIDRange{Start: uid, Stop: uid})
	return seq.Start, ok
}

func (handle *MailboxHandle[MailboxKey]) Idle(to *imapserver.UpdateWriter, done <-chan struct{}) error {
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
			if err := handle.Sync(to, true); err != nil {
				return err
			}
		case <-done:
			return nil
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
func (handle *MailboxHandle[MailboxKey]) Sync(to *imapserver.UpdateWriter, expunge bool) error {
	if handle.management {
		return nil
	}

	handle.lock.Lock()
	defer handle.lock.Unlock()

	return handle.syncUnlocked(to, expunge)
}

func (handle *MailboxHandle[MailboxKey]) syncUnlocked(to *imapserver.UpdateWriter, expunge bool) error {
	for _, upd := range handle.pendingFlags {
		seq, ok := uidToSeq(handle.uidMap, imap.UIDRange{Start: upd.uid, Stop: upd.uid})
		if !ok {
			// Likely the corresponding message was expunged.
			continue
		}

		if err := to.WriteMessageFlags(seq.Start, upd.uid, upd.newFlags); err != nil {
			return err
		}
	}
	handle.pendingFlags = make([]flagsUpdate, 0, 1)

	if expunge && len(handle.pendingExpunge) > 0 {
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
			if err := to.WriteExpunge(expunged[i]); err != nil {
				return err
			}
		}
	}

	if len(handle.pendingCreated) > 0 {
		for _, seq := range handle.pendingCreated {
			for i := seq.Start; i <= seq.Stop; i++ {
				handle.uidMap = append(handle.uidMap, i)
			}
		}

		if err := to.WriteNumMessages(uint32(len(handle.uidMap))); err != nil {
			return err
		}
		handle.pendingCreated = handle.pendingCreated[:0]

		// Order in which go-imap sends separate MailboxUpdate elements
		// is non-deterministic and depend son Items map order.
		//
		// However, imaptest wants to have RECENT always after EXISTS
		// and I believe it may indeed cause trouble for some clients,
		// so we work around it by sending multiple separate update objects.
		if handle.hasNewRecent {
			// XXX: go-imap v2 lacks support for RECENT update
			// to.WriteNumRecent(handle.recentCount)
			handle.hasNewRecent = false
		}
	}

	return nil
}

func (handle *MailboxHandle[MailboxKey]) enqueueFlagsUpdate(uid imap.UID, newFlags []imap.Flag) {
	upd := flagsUpdate{
		uid:      uid,
		newFlags: newFlags,
	}

	handle.lock.Lock()
	if handle.recent.Contains(uid) {
		upd.newFlags = make([]imap.Flag, len(newFlags))
		copy(upd.newFlags, newFlags)
		upd.newFlags = append(upd.newFlags, `\Recent`)
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

// FlagsChanged performs all necessary update dispatching
// actions on flags change.
//
// newFlags should not include \Recent, silent should be set
// if UpdateMessagesFlags was called with it set.
func (handle *MailboxHandle[MailboxKey]) FlagsChanged(uid imap.UID, newFlags []imap.Flag, silent bool) {
	if handle.m.sink != nil {
		handle.m.sink <- Update[MailboxKey]{
			Type:     UpdFlags,
			Key:      handle.key,
			SeqSet:   imap.UIDSetNum(uid),
			NewFlags: newFlags,
		}
	}

	if handle.management {
		return
	}

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
func (handle *MailboxHandle[MailboxKey]) IsRecent(uid imap.UID) bool {
	handle.lock.RLock()
	defer handle.lock.RUnlock()
	return handle.recent.Contains(uid)
}

func (handle *MailboxHandle[MailboxKey]) idleUpdate() {
	if handle.idleerNotify != nil {
		select {
		case handle.idleerNotify <- struct{}{}:
		default:
		}
	}
}

// Removed performs all necessary update dispatching actions
// for a specified removed message.
func (handle *MailboxHandle[MailboxKey]) Removed(uid imap.UID) {
	if handle.m.sink != nil {
		handle.m.sink <- Update[MailboxKey]{
			Type:   UpdRemoved,
			Key:    handle.key,
			SeqSet: imap.UIDSetNum(uid),
		}
	}

	if handle.management {
		return
	}

	handle.shared.handlesLock.RLock()
	defer handle.shared.handlesLock.RUnlock()

	for hndl := range handle.shared.handles {
		hndl.lock.Lock()
		hndl.pendingExpunge.AddNum(uid)
		hndl.idleUpdate()
		hndl.lock.Unlock()
	}
}

func (handle *MailboxHandle[MailboxKey]) RemovedSet(seq imap.UIDSet) {
	if handle.m.sink != nil {
		handle.m.sink <- Update[MailboxKey]{
			Type:   UpdRemoved,
			Key:    handle.key,
			SeqSet: seq,
		}
	}

	if handle.management {
		return
	}

	handle.shared.handlesLock.RLock()
	defer handle.shared.handlesLock.RUnlock()

	for hndl := range handle.shared.handles {
		hndl.lock.Lock()
		hndl.pendingExpunge.AddSet(seq)
		hndl.idleUpdate()
		hndl.lock.Unlock()
	}
}

func (handle *MailboxHandle[MailboxKey]) MsgsCount() int {
	return len(handle.uidMap)
}

func (handle *MailboxHandle[MailboxKey]) Close() error {
	if handle.management {
		return nil
	}

	handle.m.handlesLock.Lock()
	defer handle.m.handlesLock.Unlock()

	handle.shared.handlesLock.Lock()
	defer handle.shared.handlesLock.Unlock()

	delete(handle.shared.handles, handle)

	if len(handle.shared.handles) == 0 {
		delete(handle.m.handles, handle.shared.key)
		if handle.m.ExternalUnsubscribe != nil {
			handle.m.ExternalUnsubscribe(handle.shared.key)
		}
	}

	return nil
}
