package sequpdate

import (
	"errors"
	"sync"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
)

type flagsUpdate struct {
	silentFor *MailboxHandle
	uid       uint32
	newFlags  []string
}

type sharedHandle struct {
	key string

	handlesLock sync.RWMutex
	handles     map[*MailboxHandle]struct{}
}

type MailboxHandle struct {
	m      *Manager
	shared *sharedHandle
	conn   backend.Conn

	lock           sync.RWMutex
	uidMap         []uint32
	pendingCreated []uint32
	pendingFlags   []flagsUpdate
}

var ErrNoMessages = errors.New("No messages matched")

func (handle *MailboxHandle) ResolveSeq(uid bool, set *imap.SeqSet) (*imap.SeqSet, error) {
	handle.lock.RLock()
	defer handle.lock.RUnlock()

	if len(handle.uidMap) == 0 {
		return &imap.SeqSet{}, ErrNoMessages
	}

	if uid {
		for i, seq := range set.Set {
			if seq.Start == 0 {
				set.Set[i].Start = handle.uidMap[len(handle.uidMap)-1]
			}
			if seq.Stop == 0 {
				set.Set[i].Stop = handle.uidMap[len(handle.uidMap)-1]
			}
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

func (handle *MailboxHandle) uidAsSeq(uid uint32) (uint32, bool) {
	seq, ok := uidToSeq(handle.uidMap, imap.Seq{Start: uid, Stop: uid})
	return seq.Start, ok
}

func (handle *MailboxHandle) Sync(expunge bool) {
	handle.lock.Lock()
	defer handle.lock.Unlock()

	for _, upd := range handle.pendingFlags {
		seq, ok := handle.uidAsSeq(upd.uid)
		if !ok {
			// Likely the corresponding message was expunged.
			continue
		}
		updMsg := imap.NewMessage(seq, []imap.FetchItem{imap.FetchFlags, imap.FetchUid})
		updMsg.Flags = upd.newFlags
		updMsg.Uid = upd.uid
		handle.conn.SendUpdate(&backend.MessageUpdate{
			Message: updMsg,
		})
	}
	handle.pendingFlags = make([]flagsUpdate, 0, 1)

	if expunge {
		expunged := make([]uint32, 0, 16)
		newMap := handle.uidMap[:0] /* SliceTricks: filtering without allocations */
		for i, uid := range handle.uidMap {
			if uid == 0 {
				expunged = append(expunged, uint32(i+1))
			} else {
				newMap = append(newMap, uid)
			}
		}
		handle.uidMap = newMap

		for i := len(expunged) - 1; i >= 0; i-- {
			handle.conn.SendUpdate(&backend.ExpungeUpdate{SeqNum: expunged[i]})
		}
	}

	if len(handle.pendingCreated) != 0 {
		handle.uidMap = append(handle.uidMap, handle.pendingCreated...)
		handle.pendingCreated = make([]uint32, 0, 1)
		status := imap.NewMailboxStatus("", []imap.StatusItem{imap.StatusMessages})
		status.Messages = uint32(len(handle.uidMap))
		handle.conn.SendUpdate(&backend.MailboxUpdate{
			MailboxStatus: status,
		})
	}
}

func (handle *MailboxHandle) FlagsChanged(uid uint32, newFlags []string, silent bool) {
	upd := flagsUpdate{
		uid:      uid,
		newFlags: newFlags,
	}
	if silent {
		upd.silentFor = handle
	}

	handle.shared.handlesLock.RLock()
	defer handle.shared.handlesLock.RUnlock()

	for hndl := range handle.shared.handles {
		if upd.silentFor == handle {
			continue
		}

		hndl.lock.Lock()
		hndl.pendingFlags = append(hndl.pendingFlags)
		hndl.lock.Unlock()
	}
}

func (handle *MailboxHandle) Removed(uid uint32) {
	handle.shared.handlesLock.RLock()
	defer handle.shared.handlesLock.RUnlock()

	for hndl := range handle.shared.handles {
		hndl.lock.Lock()
		seq, ok := handle.uidAsSeq(uid)
		if ok {
			handle.uidMap[seq] = 0
		}
		hndl.lock.Unlock()
	}
}

func (handle *MailboxHandle) Close() {
	handle.m.handlesLock.Lock()
	defer handle.m.handlesLock.Unlock()

	handle.shared.handlesLock.Lock()
	defer handle.shared.handlesLock.Unlock()

	delete(handle.shared.handles, handle)

	if len(handle.shared.handles) == 0 {
		delete(handle.m.handles, handle.shared.key)
	}
}
