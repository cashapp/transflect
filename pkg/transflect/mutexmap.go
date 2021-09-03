package transflect

import "sync"

// MutexMap is map of mutexes. Access to items can be locked by name.
type MutexMap struct {
	sm sync.Map
}

// Lock acquires a lock for a given name.
func (nm *MutexMap) Lock(name string) {
	m := &sync.Mutex{}
	m.Lock()
	if v, loaded := nm.sm.LoadOrStore(name, m); loaded {
		v.(*sync.Mutex).Lock()
	}
}

// Unlock releases the lock for the given name.
func (nm *MutexMap) Unlock(name string) {
	if v, loaded := nm.sm.Load(name); loaded {
		v.(*sync.Mutex).Unlock()
	}
}

// Remove deletes the lock entry for a given name.
func (nm *MutexMap) Remove(name string) {
	nm.sm.Delete(name)
}
