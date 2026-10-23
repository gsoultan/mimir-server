package cluster

// TrainSlots reports how many training tasks the connected workers take at
// once: the capacity of every link whose worker trains. The training
// runner uses it to decide how many jobs to run together.
func (p *Pool) TrainSlots() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, l := range p.links {
		if l.trains() {
			n += int(l.hello.Capacity)
		}
	}
	return n
}

// TrainingOnly reports whether a connected worker takes training runs
// and no predictions (started with -tasks train). A model it trains must
// run elsewhere. An offline worker reports false.
func (p *Pool) TrainingOnly(workerID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		if l.worker.ID == workerID && l.trainingOnly() {
			return true
		}
	}
	return false
}

// SetTasks changes what a connected worker takes, at once: "" (as it
// was started), "all", "train" or "serve". Work already on it finishes.
func (p *Pool) SetTasks(workerID, tasks string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.links {
		if l.worker.ID == workerID {
			l.wants = tasks
			l.worker.Tasks = tasks
		}
	}
	p.wake() // a worker told to answer may take predictions that wait
}
