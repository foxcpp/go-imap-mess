package sequpdate

import (
	"errors"
	"sync"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
)

type flagsUpdate struct{
	silentFor *MailboxHandle
	uid uint32
	newFlags []string
}

type sharedHandle struct{
	key string
	
	connsLock sync.RWMutex
	conns map[backend.Conn]struct{}
	
	expunged chan uint32
	created chan uint32
	flagsUpdated chan flagsUpdate
}

type MailboxHandle struct{
	m *Manager
	shared *sharedHandle
	conn backend.Conn
	
	lock sync.RWMutex
	uidMap []uint32
	pendingCreated []uint32
	pendingFlags []flagsUpdate
}

func (handle *MailboxHandle) ResolveSeq(uid bool, set *imap.SeqSet) (*imap.SeqSet, error) {
	if uid {
		// This is done for usage convenience - backend could
		// just pass all seqsets into this function.
		return set, nil
	}
	
	handle.lock.RLock()
	defer handle.lock.RUnlock()

	result := &imap.SeqSet{}
	for _, seq := range set.Set {
		seq, ok := seqToUid(handle.uidMap, seq)
		if !ok {
			continue
		}
		result.AddRange(seq.Start, seq.Stop)
	}
	
	if len(result.Set) == 0 {
		return nil, errors.New("No messages matched")
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
		updMsg := imap.NewMessage(seq, []imap.FetchItem{imap.FetchFlags})
		updMsg.Flags = upd.newFlags
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
	handle.shared.connsLock.RLock()
	defer handle.shared.connsLock.RUnlock()

	upd := flagsUpdate{
		uid:       uid,
		newFlags:  newFlags,
	}
	if silent {
		upd.silentFor = handle
	}
	
	for range handle.shared.conns {
		handle.shared.flagsUpdated <- upd
	}
}

func (handle *MailboxHandle) Removed(uid uint32) {
	handle.shared.connsLock.RLock()
	defer handle.shared.connsLock.RUnlock()
	
	for range handle.shared.conns {
		handle.shared.expunged <- uid
	}
}

func (handle *MailboxHandle) listenUpdates() {
	for {
		select {
		case newUID := <-handle.shared.created:
			handle.pendingCreated = append(handle.pendingCreated, newUID)
		case expungedUID := <-handle.shared.expunged:
			seq, ok := handle.uidAsSeq(expungedUID)
			if ok {
				handle.uidMap[seq] = 0
			}
		case upd := <-handle.shared.flagsUpdated:
			if upd.silentFor == handle {
				continue
			}
			handle.pendingFlags = append(handle.pendingFlags, upd)
		}
	}
}

func (handle *MailboxHandle) Close() {
	handle.m.handlesLock.Lock()
	defer handle.m.handlesLock.Unlock()
	
	handle.shared.connsLock.Lock()
	defer handle.shared.connsLock.Unlock()
	
	delete(handle.shared.conns, handle.conn)
	
	if len(handle.shared.conns) == 0 {
		delete(handle.m.handles, handle.shared.key)
	}
}