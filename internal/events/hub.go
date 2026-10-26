// Package events 提供进程内的发布/订阅中心，用于 SSE 推送到浏览器。
package events

import (
	"sync"
)

type Event struct {
	Name string
	Data any
}

type Hub struct {
	mu     sync.RWMutex
	subs   map[int64]chan Event
	nextID int64
}

func NewHub() *Hub {
	return &Hub{subs: make(map[int64]chan Event)}
}

func (h *Hub) Subscribe() (int64, <-chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	ch := make(chan Event, 64)
	h.subs[id] = ch
	return id, ch
}

func (h *Hub) Unsubscribe(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(ch)
	}
}

// Publish 非阻塞广播：慢客户端会丢事件而不是阻塞任务。
func (h *Hub) Publish(name string, data any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subs {
		select {
		case ch <- Event{Name: name, Data: data}:
		default:
		}
	}
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
