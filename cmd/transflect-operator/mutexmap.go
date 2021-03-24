package main

import "sync"

type mutexMap struct {
	sm sync.Map
}

func (nm *mutexMap) lock(name string) {
	m := &sync.Mutex{}
	m.Lock()
	if v, loaded := nm.sm.LoadOrStore(name, m); loaded {
		v.(*sync.Mutex).Lock()
	}
}

func (nm *mutexMap) unlock(name string) {
	if v, loaded := nm.sm.Load(name); loaded {
		v.(*sync.Mutex).Unlock()
	}
}

func (nm *mutexMap) delete(name string) {
	nm.sm.Delete(name)
}
