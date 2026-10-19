package cluster

// TrainSlots reports how many training tasks the connected workers take at
// once: the capacity of every link whose worker trains. The training
// runner uses it to decide how many jobs to run together.
func (p *Pool) TrainSlots() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, l := range p.links {
		if l.hello.Trains {
			n += int(l.hello.Capacity)
		}
	}
	return n
}
