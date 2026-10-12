package daemon

import "sync"

// Serial 은 트리거(boot/timer/watch)가 몰려도 작업을 항상 직렬 1개 + 최대 1회 재실행으로 줄인다.
type Serial struct {
	task func(reason string)

	mu      sync.Mutex
	running bool
	rerun   *rerun
}

type rerun struct {
	reason string
	done   chan struct{}
}

// NewSerial 은 task 를 감싼 Serial 이다.
func NewSerial(task func(reason string)) *Serial { return &Serial{task: task} }

// Run 은 작업을 돌린다. 이미 돌고 있으면 재실행 1회를 예약하고 그 재실행이 끝날 때까지 기다린다.
func (s *Serial) Run(reason string) {
	s.mu.Lock()
	if s.running {
		if s.rerun == nil {
			s.rerun = &rerun{reason: reason, done: make(chan struct{})}
		}
		w := s.rerun
		s.mu.Unlock()
		<-w.done
		return
	}
	s.running = true
	s.mu.Unlock()

	var finished *rerun
	for {
		s.task(reason)
		if finished != nil {
			close(finished.done)
		}
		s.mu.Lock()
		next := s.rerun
		s.rerun = nil
		if next == nil {
			s.running = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		reason, finished = next.reason, next
	}
}
